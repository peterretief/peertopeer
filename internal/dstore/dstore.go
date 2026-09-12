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
	DataShards          int
	ParityShards        int
	Members             []string
	MinPeers            int
	RequireCapabilities bool
	KeepOriginal        bool
	MaxFileBytes        int64
	ChunkSize           int64
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

func layoutForConfig(cfg Config) (erasure.Layout, error) {
	return erasure.NewLayout(cfg.DataShards, cfg.ParityShards)
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
	layout, err := layoutForConfig(cfg)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("invalid erasure layout: %w", err)
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
	if limit > 1<<40 {
		return ProcessResult{}, fmt.Errorf("file limit is too large: %d", limit)
	}
	if info.Size() > limit {
		return ProcessResult{}, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 16 << 20
	}
	if chunkSize > int64(^uint(0)>>1) || chunkSize > 1<<30 {
		return ProcessResult{}, fmt.Errorf("chunk size is too large: %d", chunkSize)
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

	key := make([]byte, cryptofile.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return ProcessResult{}, fmt.Errorf("generate key: %w", err)
	}
	port := cfg.ListenPort
	if port == "" {
		port = "8080"
	}
	configuredPeers := len(cfg.Peers) > 0
	capChunkSize := chunkSize
	if info.Size() < capChunkSize {
		capChunkSize = info.Size()
	}
	maxShardBytes := int64((capChunkSize + 16 + int64(layout.DataShards) - 1) / int64(layout.DataShards))
	peerList, err := shardPeers(ctx, cfg, layout.TotalShards(), maxShardBytes)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("cannot place shards: %w", err)
	}
	store := localstore.WithQuota(cfg.ShardDir, cfg.QuotaBytes)
	if cfg.Store != nil {
		store = *cfg.Store
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	reader := io.LimitReader(f, limit+1)
	buffer := make([]byte, int(chunkSize))
	chunks := make([]manifest.Chunk, 0, int((info.Size()+chunkSize-1)/chunkSize))
	var plaintextTotal int64
	for {
		if err := ctx.Err(); err != nil {
			return ProcessResult{}, err
		}
		n, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return ProcessResult{}, fmt.Errorf("read original: %w", readErr)
		}
		if n > 0 || plaintextTotal == 0 {
			plaintextTotal += int64(n)
			if plaintextTotal > limit {
				return ProcessResult{}, fmt.Errorf("file exceeds %d-byte limit", limit)
			}
			ciphertext, nonce, err := cryptofile.EncryptChunk(buffer[:n], key)
			if err != nil {
				return ProcessResult{}, err
			}
			shards, err := layout.Encode(ciphertext)
			if err != nil {
				return ProcessResult{}, err
			}
			refs := make([]manifest.ShardRef, layout.TotalShards())
			for i, shard := range shards {
				peerName := "local"
				var shardURLValue string
				if i < len(peerList) {
					p := peerList[i]
					peerName = p.HostName
					peerPort := port
					if cfg.PeerPorts != nil {
						if custom, ok := cfg.PeerPorts[p.HostName]; ok {
							peerPort = custom
						}
					}
					shardURLValue = shardURL(p, peerPort, manifest.Hash(shard))
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
				ref := manifest.ShardRef{Hash: manifest.Hash(shard), Peer: peerName}
				if shardURLValue != "" {
					ref.URL = shardURLValue
				} else if baseURL != "" {
					ref.URL = baseURL + "/shards/" + ref.Hash
				} else {
					ref.URL = "local"
				}
				refs[i] = ref
				// The shard has been uploaded and persisted; release its chunk buffer
				// before moving to the next shard.
				shards[i] = nil
			}
			chunks = append(chunks, manifest.Chunk{PlaintextSize: n, CiphertextSize: len(ciphertext), Nonce: nonce, Shards: refs})
			ciphertext = nil
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	fileID, err := randomID()
	if err != nil {
		return ProcessResult{}, err
	}
	m, err := manifest.NewChunked(layout, fileID, filepath.Base(path), int(plaintextTotal), int(chunkSize), key, chunks)
	if err != nil {
		return ProcessResult{}, err
	}
	m.ShareID = cfg.ShareID
	stubBytes, err := manifest.Marshal(m)
	if err != nil {
		return ProcessResult{}, err
	}

	current, err := os.Lstat(path)
	if err != nil {
		return ProcessResult{}, err
	}
	if !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) || plaintextTotal != info.Size() {
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

	resultManifest := m
	// Keep the first chunk's placement available to callers of ProcessResult for
	// compatibility with the pre-chunked API. The on-disk manifest stores refs
	// per chunk and does not duplicate this field.
	if len(chunks) > 0 {
		resultManifest.Shards = append([]manifest.ShardRef(nil), chunks[0].Shards...)
	}
	return ProcessResult{OriginalPath: path, StubPath: stubPath, Manifest: resultManifest}, nil
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
	if m.Version == 2 {
		return restoreChunked(ctx, m, shardDir, outputPath)
	}

	layout, err := erasure.NewLayout(m.DataShards, m.ParityShards)
	if err != nil {
		return "", fmt.Errorf("invalid manifest erasure layout: %w", err)
	}
	var store localstore.Store
	if shardDir != "" {
		store = localstore.New(shardDir)
	}
	client := shardClient()
	shards := make([][]byte, layout.TotalShards())
	maxShardBytes := int64((m.CiphertextSize + layout.DataShards - 1) / layout.DataShards)
	for i, ref := range m.Shards {
		if ref.Hash == "" {
			continue
		}
		shard, err := fetchShard(ctx, client, store, shardDir != "", ref, m.ShareID, maxShardBytes)
		if err != nil {
			continue
		}
		shards[i] = shard
	}

	ciphertext, err := layout.Decode(shards, m.CiphertextSize)
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

func restoreChunked(ctx context.Context, m manifest.Manifest, shardDir, outputPath string) (string, error) {
	layout, err := erasure.NewLayout(m.DataShards, m.ParityShards)
	if err != nil {
		return "", fmt.Errorf("invalid manifest erasure layout: %w", err)
	}
	var store localstore.Store
	if shardDir != "" {
		store = localstore.New(shardDir)
	}
	client := shardClient()
	err = atomicfile.WriteNewFrom(outputPath, 0o600, func(w io.Writer) error {
		for chunkIndex, chunk := range m.Chunks {
			if err := ctx.Err(); err != nil {
				return err
			}
			shards := make([][]byte, layout.TotalShards())
			maxShardBytes := int64((chunk.CiphertextSize + layout.DataShards - 1) / layout.DataShards)
			for i, ref := range chunk.Shards {
				if ref.Hash == "" {
					continue
				}
				shard, err := fetchShard(ctx, client, store, shardDir != "", ref, m.ShareID, maxShardBytes)
				if err != nil {
					continue
				}
				shards[i] = shard
			}
			ciphertext, err := layout.Decode(shards, chunk.CiphertextSize)
			shards = nil
			if err != nil {
				return fmt.Errorf("decode chunk %d: %w", chunkIndex, err)
			}
			plaintext, err := cryptofile.Decrypt(ciphertext, m.Key, chunk.Nonce)
			if err != nil {
				return fmt.Errorf("decrypt chunk %d: %w", chunkIndex, err)
			}
			if len(plaintext) != chunk.PlaintextSize {
				return fmt.Errorf("chunk %d plaintext size mismatch: got %d want %d", chunkIndex, len(plaintext), chunk.PlaintextSize)
			}
			if _, err := w.Write(plaintext); err != nil {
				return fmt.Errorf("write chunk %d: %w", chunkIndex, err)
			}
		}
		return nil
	})
	if err != nil {
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
