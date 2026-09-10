package dstore

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AddPath retains every source and puts manifests in a separate library.
// A directory batch can partially succeed; returned results identify its files.
func AddPath(ctx context.Context, source, library string, cfg Config) ([]ProcessResult, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(library, 0o700); err != nil {
		return nil, err
	}
	library, err = filepath.Abs(library)
	if err != nil {
		return nil, err
	}
	library, err = filepath.EvalSymlinks(library)
	if err != nil {
		return nil, err
	}
	if pathWithin(source, library) || pathWithin(library, source) {
		return nil, fmt.Errorf("source and manifest library must not overlap")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	cfg.KeepOriginal = true
	target := filepath.Join(library, filepath.Base(source))
	if !info.IsDir() {
		result, err := processFile(ctx, source, target+StubExtension, cfg)
		if err != nil {
			return nil, err
		}
		return []ProcessResult{result}, nil
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return nil, fmt.Errorf("create new manifest directory: %w", err)
	}
	var results []ProcessResult
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported entry (links and special files are not followed): %s", path)
		}
		result, err := processFile(ctx, path, dest+StubExtension, cfg)
		if err != nil {
			return fmt.Errorf("add %s: %w", path, err)
		}
		results = append(results, result)
		return nil
	})
	return results, err
}

func RestoreDirectory(ctx context.Context, source, output, shards string) ([]string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if pathWithin(source, output) || pathWithin(output, source) {
		return nil, fmt.Errorf("restore source and output must not overlap")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("restore source must be a directory")
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return nil, fmt.Errorf("restore output must be a new directory: %w", err)
	}
	var restored []string
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(output, rel), 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported manifest entry: %s", path)
		}
		if !strings.HasSuffix(path, StubExtension) {
			return nil
		}
		out, err := RestoreFile(ctx, path, shards, filepath.Join(output, strings.TrimSuffix(rel, StubExtension)))
		if err != nil {
			return fmt.Errorf("restore %s: %w", path, err)
		}
		restored = append(restored, out)
		return nil
	})
	return restored, err
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
