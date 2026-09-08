package dstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peterretief/peertopeer/internal/cryptofile"
	"github.com/peterretief/peertopeer/internal/erasure"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
)

const StubExtension = ".dstore"

type Config struct {
	OriginDir string
	ShardDir  string
	BaseURL   string
}

type ProcessResult struct {
	OriginalPath string
	StubPath     string
	Manifest     manifest.Manifest
}

func ProcessDirectory(cfg Config) ([]ProcessResult, error) {
	if cfg.OriginDir == "" {
		return nil, errors.New("origin directory is required")
	}
	entries, err := os.ReadDir(cfg.OriginDir)
	if err != nil {
		return nil, fmt.Errorf("read origin directory: %w", err)
	}

	var results []ProcessResult
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), StubExtension) {
			continue
		}
		path := filepath.Join(cfg.OriginDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		result, err := ProcessFileWithConfig(path, cfg)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func ProcessFile(path, shardDir string) (ProcessResult, error) {
	return ProcessFileWithConfig(path, Config{ShardDir: shardDir})
}

func ProcessFileWithConfig(path string, cfg Config) (ProcessResult, error) {
	if strings.HasSuffix(path, StubExtension) {
		return ProcessResult{}, fmt.Errorf("refusing to process stub file: %s", path)
	}
	if cfg.ShardDir == "" {
		return ProcessResult{}, errors.New("shard directory is required")
	}

	info, err := os.Stat(path)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("stat original: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ProcessResult{}, fmt.Errorf("not a regular file: %s", path)
	}

	plaintext, err := os.ReadFile(path)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("read original: %w", err)
	}
	ciphertext, key, nonce, err := cryptofile.Encrypt(plaintext)
	if err != nil {
		return ProcessResult{}, err
	}
	shards, err := erasure.Encode(ciphertext)
	if err != nil {
		return ProcessResult{}, err
	}

	store := localstore.New(cfg.ShardDir)
	peers := make([]string, erasure.TotalShards)
	for i, shard := range shards {
		if _, err := store.Put(shard); err != nil {
			return ProcessResult{}, err
		}
		peers[i] = "local"
	}

	fileID, err := randomID()
	if err != nil {
		return ProcessResult{}, err
	}
	m, err := manifest.New(fileID, filepath.Base(path), len(ciphertext), key, nonce, shards, peers)
	if err != nil {
		return ProcessResult{}, err
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL != "" {
		for i := range m.Shards {
			m.Shards[i].URL = baseURL + "/shards/" + m.Shards[i].Hash
		}
	}
	stubBytes, err := manifest.Marshal(m)
	if err != nil {
		return ProcessResult{}, err
	}

	stubPath := path + StubExtension
	if err := os.WriteFile(stubPath, stubBytes, 0o600); err != nil {
		return ProcessResult{}, fmt.Errorf("write stub: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return ProcessResult{}, fmt.Errorf("remove original after stub write: %w", err)
	}

	return ProcessResult{OriginalPath: path, StubPath: stubPath, Manifest: m}, nil
}

func RestoreFile(stubPath, shardDir, outputPath string) (string, error) {
	stubBytes, err := os.ReadFile(stubPath)
	if err != nil {
		return "", fmt.Errorf("read stub: %w", err)
	}
	m, err := manifest.Unmarshal(stubBytes)
	if err != nil {
		return "", err
	}
	if outputPath == "" {
		outputPath = strings.TrimSuffix(stubPath, StubExtension)
		if outputPath == stubPath {
			outputPath = filepath.Join(filepath.Dir(stubPath), m.FileName)
		}
	}

	var store localstore.Store
	if shardDir != "" {
		store = localstore.New(shardDir)
	}
	client := http.Client{Timeout: 30 * time.Second}
	shards := make([][]byte, erasure.TotalShards)
	for i, ref := range m.Shards {
		if ref.Hash == "" {
			continue
		}
		shard, err := fetchShard(client, store, shardDir != "", ref)
		if err != nil {
			continue
		}
		shards[i] = shard
	}

	ciphertext, err := erasure.Decode(shards, m.CiphertextSize)
	if err != nil {
		return "", err
	}
	plaintext, err := cryptofile.Decrypt(ciphertext, m.Key, m.Nonce)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(outputPath, plaintext, 0o600); err != nil {
		return "", fmt.Errorf("write restored file: %w", err)
	}
	return outputPath, nil
}

func Watch(cfg Config, interval time.Duration, onProcessed func(ProcessResult)) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if err := os.MkdirAll(cfg.OriginDir, 0o755); err != nil {
		return fmt.Errorf("create origin directory: %w", err)
	}
	if err := os.MkdirAll(cfg.ShardDir, 0o755); err != nil {
		return fmt.Errorf("create shard directory: %w", err)
	}

	for {
		results, err := ProcessDirectory(cfg)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, result := range results {
			if onProcessed != nil {
				onProcessed(result)
			}
		}
		time.Sleep(interval)
	}
}

func fetchShard(client http.Client, store localstore.Store, allowLocal bool, ref manifest.ShardRef) ([]byte, error) {
	if ref.URL != "" {
		data, err := downloadShard(client, ref.URL)
		if err == nil && manifest.Hash(data) == ref.Hash {
			return data, nil
		}
	}
	if allowLocal {
		return store.Get(ref.Hash)
	}
	return nil, fmt.Errorf("shard unavailable: %s", ref.Hash)
}

func downloadShard(client http.Client, rawURL string) ([]byte, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate file id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
