package erasure

import (
	"fmt"

	"github.com/klauspost/reedsolomon"
)

const (
	DataShards   = 2
	ParityShards = 1
	TotalShards  = DataShards + ParityShards
)

func Encode(data []byte) ([][]byte, error) {
	enc, err := reedsolomon.New(DataShards, ParityShards)
	if err != nil {
		return nil, fmt.Errorf("new encoder: %w", err)
	}

	shards, err := enc.Split(data)
	if err != nil {
		return nil, fmt.Errorf("split: %w", err)
	}
	if err := enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return shards, nil
}

func Decode(shards [][]byte, outputSize int) ([]byte, error) {
	if len(shards) != TotalShards {
		return nil, fmt.Errorf("invalid shard count: got %d want %d", len(shards), TotalShards)
	}

	enc, err := reedsolomon.New(DataShards, ParityShards)
	if err != nil {
		return nil, fmt.Errorf("new encoder: %w", err)
	}
	if err := enc.Reconstruct(shards); err != nil {
		return nil, fmt.Errorf("reconstruct: %w", err)
	}

	out := make([]byte, 0, outputSize)
	writer := appendWriter{buf: &out}
	if err := enc.Join(writer, shards, outputSize); err != nil {
		return nil, fmt.Errorf("join: %w", err)
	}
	return out, nil
}

type appendWriter struct {
	buf *[]byte
}

func (w appendWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
