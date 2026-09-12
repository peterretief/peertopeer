package nodeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/peterretief/peertopeer/internal/atomicfile"
	"github.com/peterretief/peertopeer/internal/protocol"
)

type Config struct {
	Version       int      `json:"version"`
	Name          string   `json:"name"`
	ShareID       string   `json:"share_id"`
	Role          string   `json:"role"`
	Members       []string `json:"members"`
	AllowMesh     bool     `json:"allow_mesh"`
	Port          string   `json:"port"`
	OriginDir     string   `json:"origin_dir"`
	ShardDir      string   `json:"shard_dir"`
	LibraryDir    string   `json:"library_dir"`
	QuotaBytes    int64    `json:"quota_bytes"`
	MaxFileBytes  int64    `json:"max_file_bytes"`
	ChunkSize     int64    `json:"chunk_size,omitempty"`
	MaxShardBytes int64    `json:"max_shard_bytes"`
	Peers         string   `json:"peers,omitempty"`
	PeerPorts     string   `json:"peer_ports,omitempty"`
}

func Default(name string) Config {
	return Config{Version: 1, Name: name, ShareID: "personal", Role: "storage", Port: "8080",
		Members: []string{}, OriginDir: "outfiles", ShardDir: ".dstore-shards", LibraryDir: "library",
		QuotaBytes: 10 << 30, MaxFileBytes: 64 << 20, ChunkSize: 16 << 20, MaxShardBytes: protocol.DefaultMaxShardBytes}
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported configuration version %d", c.Version)
	}
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.ShareID) == "" {
		return fmt.Errorf("name and share_id are required")
	}
	if c.Role != "storage" && c.Role != "client" {
		return fmt.Errorf("role must be storage or client")
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if c.QuotaBytes <= 0 || c.MaxFileBytes <= 0 || c.MaxShardBytes <= 0 || c.MaxFileBytes > 1<<40 || c.MaxShardBytes > 1<<30 || c.ChunkSize < 0 || c.ChunkSize > 1<<30 {
		return fmt.Errorf("quota must be positive; file limit must be between 1 byte and 1 TiB, shard limit between 1 byte and 1 GiB")
	}
	if !c.AllowMesh && len(c.Members) == 0 {
		return fmt.Errorf("list at least one member, or explicitly enable allow_mesh")
	}
	for _, member := range c.Members {
		if strings.TrimSpace(member) == "" || strings.ContainsAny(member, "\r\n\t") {
			return fmt.Errorf("invalid member identity %q", member)
		}
	}
	paths := []string{c.OriginDir, c.ShardDir, c.LibraryDir}
	for i, path := range paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("origin, shard, and library directories are required")
		}
		for _, other := range paths[:i] {
			if contains(path, other) || contains(other, path) {
				return fmt.Errorf("origin, shard, and library directories must not overlap")
			}
		}
	}
	return nil
}

func contains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("config must contain one JSON object")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	for _, p := range []*string{&c.OriginDir, &c.ShardDir, &c.LibraryDir} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(abs), *p)
		}
	}
	return c, c.Validate()
}

func SaveNew(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteNew(path, append(data, '\n'), 0o600)
}

func (c Config) ListenAddr(ip string) string { return net.JoinHostPort(ip, c.Port) }
