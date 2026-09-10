package dstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peterretief/peertopeer/internal/atomicfile"
	"github.com/peterretief/peertopeer/internal/cryptofile"
	"github.com/peterretief/peertopeer/internal/erasure"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/peer"
	"github.com/peterretief/peertopeer/internal/protocol"
)

const StubExtension = ".dstore"

type Config struct {
	OriginDir           string
	ShardDir            string
	BaseURL             string
	ListenPort          string
	PeerPorts           map[string]string
	Peers               []peer.Peer
	ExcludePeers        []string
	ShareID             string
	Members             []string
	MinPeers            int
	RequireCapabilities bool
	KeepOriginal        bool
	MaxFileBytes        int64
	QuotaBytes          int64
	Store               *localstore.Store
	Discover            func(context.Context) ([]peer.Peer, error)
	OnError             func(error)
	ready               func(string, fs.FileInfo) bool
}

type ProcessResult struct {
	OriginalPath string
	StubPath     string
	Manifest     manifest.Manifest
}

func ProcessDirectory(ctx context.Context, cfg Config) ([]ProcessResult, error) {
	if cfg.OriginDir == "" {
		return nil, errors.New("origin directory is required")
	}

	var results []ProcessResult
	var failures []error
	err := filepath.WalkDir(cfg.OriginDir, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if path != cfg.OriginDir && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), StubExtension) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if _, err := os.Lstat(path + StubExtension); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if cfg.ready != nil && !cfg.ready(path, info) {
			return nil
		}

		result, err := ProcessFileWithConfig(ctx, path, cfg)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		results = append(results, result)
		return nil
	})
	if err != nil {
		failures = append(failures, fmt.Errorf("walk origin directory: %w", err))
	}
	return results, errors.Join(failures...)
}

func ProcessFile(ctx context.Context, path, shardDir string) (ProcessResult, error) {
	return ProcessFileWithConfig(ctx, path, Config{ShardDir: shardDir})
}

func ProcessFileWithConfig(ctx context.Context, path string, cfg Config) (ProcessResult, error) {
	return processFile(ctx, path, path+StubExtension, cfg)
}

func processFile(ctx context.Context, path, stubPath string, cfg Config) (ProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return ProcessResult{}, err
	}
	if strings.HasSuffix(path, StubExtension) {
		return ProcessResult{}, fmt.Errorf("refusing to process stub file: %s", path)
	}
	if cfg.ShardDir == "" {
		return ProcessResult{}, errors.New("shard directory is required")
	}

	if _, err := os.Lstat(stubPath); err == nil {
		return ProcessResult{}, fmt.Errorf("manifest already exists: %s", stubPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProcessResult{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("stat original: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ProcessResult{}, fmt.Errorf("not a regular file: %s", path)
	}

	limit := cfg.MaxFileBytes
	if limit <= 0 {
		limit = 64 << 20
	}
	if info.Size() > limit {
		return ProcessResult{}, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return ProcessResult{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return ProcessResult{}, err
	}
	if !os.SameFile(info, opened) {
		return ProcessResult{}, fmt.Errorf("source changed before reading")
	}
	plaintext, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return ProcessResult{}, fmt.Errorf("read original: %w", err)
	}
	if int64(len(plaintext)) > limit {
		return ProcessResult{}, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	ciphertext, key, nonce, err := cryptofile.Encrypt(plaintext)
	if err != nil {
		return ProcessResult{}, err
	}
	shards, err := erasure.Encode(ciphertext)
	if err != nil {
		return ProcessResult{}, err
	}

	configuredPeers := len(cfg.Peers) > 0
	peerList, peerErr := shardPeers(ctx, cfg, erasure.TotalShards, int64(len(shards[0])))
	if peerErr != nil {
		return ProcessResult{}, fmt.Errorf("cannot place shards: %w", peerErr)
	}

	port := cfg.ListenPort
	if port == "" {
		port = "8080"
	}

	store := localstore.WithQuota(cfg.ShardDir, cfg.QuotaBytes)
	if cfg.Store != nil {
		store = *cfg.Store
	}
	peerURLs := make([]string, erasure.TotalShards)
	peerNames := make([]string, erasure.TotalShards)
	for i, shard := range shards {
		peerNames[i] = "local"

		if peerErr == nil && i < len(peerList) {
			p := peerList[i]
			peerNames[i] = p.HostName
			peerPort := port
			if cfg.PeerPorts != nil {
				if custom, ok := cfg.PeerPorts[p.HostName]; ok {
					peerPort = custom
				}
			}
			peerURLs[i] = shardURL(p, peerPort, manifest.Hash(shard))
			if err := pushShard(ctx, p, shard, peerPort, cfg.ShareID); err != nil {
				if configuredPeers {
					return ProcessResult{}, fmt.Errorf("configured peer %s unavailable for shard %d: %w", p.HostName, i, err)
				}
				return ProcessResult{}, fmt.Errorf("peer %s unavailable for shard %d: %w", p.HostName, i, err)
			}
		}

		if _, err := store.Put(shard); err != nil {
			return ProcessResult{}, fmt.Errorf("store shard %d: %w", i, err)
		}
	}

	fileID, err := randomID()
	if err != nil {
		return ProcessResult{}, err
	}
	m, err := manifest.New(fileID, filepath.Base(path), len(ciphertext), key, nonce, shards, peerNames)
	if err != nil {
		return ProcessResult{}, err
	}

	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	m.ShareID = cfg.ShareID
	for i := range m.Shards {
		if peerURLs[i] != "" {
			m.Shards[i].URL = peerURLs[i]
		} else if baseURL != "" {
			m.Shards[i].URL = baseURL + "/shards/" + m.Shards[i].Hash
		} else {
			m.Shards[i].URL = "local"
		}
	}

	stubBytes, err := manifest.Marshal(m)
	if err != nil {
		return ProcessResult{}, err
	}

	current, err := os.Lstat(path)
	if err != nil {
		return ProcessResult{}, err
	}
	if !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) || int64(len(plaintext)) != info.Size() {
		return ProcessResult{}, fmt.Errorf("source changed during upload; original retained")
	}
	if err := ctx.Err(); err != nil {
		return ProcessResult{}, err
	}
	if err := atomicfile.WriteNew(stubPath, stubBytes, 0o600); err != nil {
		return ProcessResult{}, fmt.Errorf("write stub: %w", err)
	}
	if !cfg.KeepOriginal {
		if err := os.Remove(path); err != nil {
			return ProcessResult{}, fmt.Errorf("remove original after stub write: %w", err)
		}
	}

	return ProcessResult{OriginalPath: path, StubPath: stubPath, Manifest: m}, nil
}

func pushShard(ctx context.Context, p peer.Peer, shard []byte, port, shareID string) error {
	url := shardURL(p, port, manifest.Hash(shard))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(shard))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(protocol.ShareHeader, shareID)

	client := shardClient()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("put %s: %w", url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("put %s: %s", url, resp.Status)
	}
	data, err := downloadShard(ctx, client, url, shareID, int64(len(shard)))
	if err != nil {
		return fmt.Errorf("verify remote shard: %w", err)
	}
	if got, want := manifest.Hash(data), manifest.Hash(shard); got != want {
		return fmt.Errorf("verify remote shard: hash mismatch got %s want %s", got, want)
	}
	return nil
}

func shardPeers(ctx context.Context, cfg Config, count int, shardBytes int64) ([]peer.Peer, error) {
	var candidates []peer.Peer
	if len(cfg.Peers) > 0 {
		candidates = append(candidates, cfg.Peers...)
	} else {
		discover := cfg.Discover
		if discover == nil {
			discover = peer.OnlinePeers
		}
		var err error
		candidates, err = discover(ctx)
		if err != nil {
			return nil, err
		}
	}
	var eligible []peer.Peer
	seen := make(map[string]bool)
	for _, p := range candidates {
		if !p.Online || containsName(cfg.ExcludePeers, p.HostName) || (len(cfg.Members) > 0 && !containsName(cfg.Members, p.HostName)) {
			continue
		}
		// IP, not hostname, represents a failure domain on the VPN.
		id := p.TailIP
		if cfg.MinPeers == 0 && len(cfg.Peers) > 0 {
			id = net.JoinHostPort(p.TailIP, peer.Port(p, cfg.ListenPort, cfg.PeerPorts))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		eligible = append(eligible, p)
	}
	if cfg.RequireCapabilities || len(cfg.Peers) == 0 {
		statuses := peer.Inspect(ctx, eligible, cfg.ListenPort, cfg.PeerPorts, cfg.ShareID)
		eligible = nil
		for _, status := range statuses {
			if status.Ready && status.Info.MaxShardBytes >= shardBytes && (status.Info.QuotaBytes == 0 || status.Info.QuotaBytes-status.Info.UsedBytes >= shardBytes) {
				eligible = append(eligible, status.Peer)
			}
		}
	}
	required := cfg.MinPeers
	if required == 0 {
		required = 1
		if len(cfg.Peers) == 0 {
			required = count
		}
	}
	if len(eligible) < required {
		return nil, fmt.Errorf("ready storage devices: got %d, need %d; run dstore peers -check with the same config", len(eligible), required)
	}
	if len(cfg.Peers) == 0 {
		mathrand.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	}
	assigned := make([]peer.Peer, count)
	for i := range assigned {
		assigned[i] = eligible[i%len(eligible)]
	}
	return assigned, nil
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func shardClient() http.Client {
	return http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func shardURL(p peer.Peer, port, hash string) string {
	return "http://" + net.JoinHostPort(p.TailIP, port) + "/shards/" + hash
}

func RestoreFile(ctx context.Context, stubPath, shardDir, outputPath string) (string, error) {
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
	client := shardClient()
	shards := make([][]byte, erasure.TotalShards)
	for i, ref := range m.Shards {
		if ref.Hash == "" {
			continue
		}
		shard, err := fetchShard(ctx, client, store, shardDir != "", ref, m.ShareID, int64((m.CiphertextSize+erasure.DataShards-1)/erasure.DataShards))
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
	if err := atomicfile.WriteNew(outputPath, plaintext, 0o600); err != nil {
		return "", fmt.Errorf("write restored file: %w", err)
	}
	return outputPath, nil
}

func Watch(ctx context.Context, cfg Config, interval time.Duration, onProcessed func(ProcessResult)) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if err := os.MkdirAll(cfg.OriginDir, 0o755); err != nil {
		return fmt.Errorf("create origin directory: %w", err)
	}
	if err := os.MkdirAll(cfg.ShardDir, 0o755); err != nil {
		return fmt.Errorf("create shard directory: %w", err)
	}

	previous := make(map[string]fs.FileInfo)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		observed := make(map[string]fs.FileInfo)
		cfg.ready = func(path string, info fs.FileInfo) bool {
			observed[path] = info
			old := previous[path]
			return old != nil && os.SameFile(old, info) && old.Size() == info.Size() && old.ModTime().Equal(info.ModTime())
		}
		results, err := ProcessDirectory(ctx, cfg)
		previous = observed
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if cfg.OnError != nil {
				cfg.OnError(err)
			} else {
				fmt.Fprintln(os.Stderr, "dstore: pending:", err)
			}
		}
		for _, result := range results {
			if onProcessed != nil {
				onProcessed(result)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func fetchShard(ctx context.Context, client http.Client, store localstore.Store, allowLocal bool, ref manifest.ShardRef, shareID string, maxBytes int64) ([]byte, error) {
	if ref.URL != "" {
		data, err := downloadShard(ctx, client, ref.URL, shareID, maxBytes)
		if err == nil && manifest.Hash(data) == ref.Hash {
			return data, nil
		}
	}
	if allowLocal {
		return store.Get(ref.Hash)
	}
	return nil, fmt.Errorf("shard unavailable: %s", ref.Hash)
}

func downloadShard(ctx context.Context, client http.Client, rawURL, shareID string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(protocol.ShareHeader, shareID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("shard exceeds expected size")
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
