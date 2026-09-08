package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/peterretief/peertopeer/internal/erasure"
)

type Manifest struct {
	FileID         string                        `json:"file_id"`
	FileName       string                        `json:"file_name"`
	CiphertextSize int                           `json:"ciphertext_size"`
	Key            []byte                        `json:"key"`
	Nonce          []byte                        `json:"nonce"`
	Shards         [erasure.TotalShards]ShardRef `json:"shards"`
}

type ShardRef struct {
	Hash string `json:"hash"`
	Peer string `json:"peer"`
}

func New(fileID, fileName string, ciphertextSize int, key, nonce []byte, shards [][]byte, peers []string) (Manifest, error) {
	if len(shards) != erasure.TotalShards {
		return Manifest{}, fmt.Errorf("invalid shard count: got %d want %d", len(shards), erasure.TotalShards)
	}
	if len(peers) != erasure.TotalShards {
		return Manifest{}, fmt.Errorf("invalid peer count: got %d want %d", len(peers), erasure.TotalShards)
	}

	m := Manifest{
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
	return m, nil
}
