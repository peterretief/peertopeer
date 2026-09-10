package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/peterretief/peertopeer/internal/erasure"
)

type Manifest struct {
	Version        int        `json:"version,omitempty"`
	DataShards     int        `json:"data_shards,omitempty"`
	ParityShards   int        `json:"parity_shards,omitempty"`
	ShareID        string     `json:"share_id,omitempty"`
	FileID         string     `json:"file_id"`
	FileName       string     `json:"file_name"`
	CiphertextSize int        `json:"ciphertext_size"`
	Key            []byte     `json:"key"`
	Nonce          []byte     `json:"nonce"`
	Shards         []ShardRef `json:"shards"`
}

type ShardRef struct {
	Hash string `json:"hash"`
	Peer string `json:"peer"`
	URL  string `json:"url,omitempty"`
}

func New(fileID, fileName string, ciphertextSize int, key, nonce []byte, shards [][]byte, peers []string) (Manifest, error) {
	if len(shards) != erasure.TotalShards {
		return Manifest{}, fmt.Errorf("invalid shard count: got %d want %d", len(shards), erasure.TotalShards)
	}
	if len(peers) != erasure.TotalShards {
		return Manifest{}, fmt.Errorf("invalid peer count: got %d want %d", len(peers), erasure.TotalShards)
	}

	m := Manifest{
		Version: 1, DataShards: erasure.DataShards, ParityShards: erasure.ParityShards,
		Shards:         make([]ShardRef, erasure.TotalShards),
		FileID:         fileID,
		FileName:       fileName,
		CiphertextSize: ciphertextSize,
		Key:            append([]byte(nil), key...),
		Nonce:          append([]byte(nil), nonce...),
	}
	for i, shard := range shards {
		m.Shards[i] = ShardRef{
			Hash: Hash(shard),
			Peer: peers[i],
		}
	}
	return m, nil
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Marshal(m Manifest) ([]byte, error) {
	stub, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return append(stub, '\n'), nil
}

func Unmarshal(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("unmarshal manifest: %w", err)
	}
	if m.Version != 0 && m.Version != 1 {
		return m, fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if m.Version == 0 {
		m.DataShards, m.ParityShards = erasure.DataShards, erasure.ParityShards
	}
	if m.DataShards != erasure.DataShards || m.ParityShards != erasure.ParityShards || len(m.Shards) != erasure.TotalShards {
		return m, fmt.Errorf("unsupported shard layout; this agent supports %d+%d", erasure.DataShards, erasure.ParityShards)
	}
	if m.FileName == "" || m.FileName == "." || m.FileName == ".." || filepath.Base(m.FileName) != m.FileName || strings.ContainsAny(m.FileName, "/\\\x00") {
		return m, fmt.Errorf("invalid manifest filename")
	}
	if len(m.Key) != 32 || len(m.Nonce) != 12 || m.CiphertextSize < 16 || m.CiphertextSize > (1<<30)+16 {
		return m, fmt.Errorf("invalid manifest encryption parameters or size")
	}
	for _, ref := range m.Shards {
		if len(ref.Hash) != 64 {
			return m, fmt.Errorf("invalid shard hash")
		}
		if _, err := hex.DecodeString(ref.Hash); err != nil {
			return m, fmt.Errorf("invalid shard hash: %w", err)
		}
	}
	return m, nil
}
