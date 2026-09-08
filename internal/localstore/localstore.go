package localstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterretief/peertopeer/internal/manifest"
)

type Store struct {
	dir string
}

func New(dir string) Store {
	return Store{dir: dir}
}

func (s Store) Put(shard []byte) (string, error) {
	hash := manifest.Hash(shard)
	if err := validateHash(hash); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("create store: %w", err)
	}

	path := filepath.Join(s.dir, hash)
	existing, err := os.ReadFile(path)
	if err == nil {
		if manifest.Hash(existing) != hash {
			return "", fmt.Errorf("existing shard hash mismatch for %s", hash)
		}
		return hash, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read existing shard: %w", err)
	}
	if err := os.WriteFile(path, shard, 0o644); err != nil {
		return "", fmt.Errorf("write shard: %w", err)
	}
	return hash, nil
}

func (s Store) Get(hash string) ([]byte, error) {
	if err := validateHash(hash); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, hash))
	if err != nil {
		return nil, fmt.Errorf("read shard %s: %w", hash, err)
	}
	if got := manifest.Hash(data); got != hash {
		return nil, fmt.Errorf("shard hash mismatch: got %s want %s", got, hash)
	}
	return data, nil
}

func validateHash(hash string) error {
	if len(hash) != 64 {
		return fmt.Errorf("invalid hash length: %d", len(hash))
	}
	if strings.ContainsAny(hash, `/\\`) {
		return fmt.Errorf("invalid hash path segment: %q", hash)
	}
	return nil
}
