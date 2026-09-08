package internal_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/peterretief/peertopeer/internal/cryptofile"
	"github.com/peterretief/peertopeer/internal/erasure"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
)

func TestLocalEncryptShardStubAndReconstruct(t *testing.T) {
	plaintext := bytes.Repeat([]byte("peer-to-peer storage test payload\n"), 1024)

	ciphertext, key, nonce, err := cryptofile.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plaintext[:32]) {
		t.Fatal("ciphertext contains visible plaintext prefix")
	}

	shards, err := erasure.Encode(ciphertext)
	if err != nil {
		t.Fatal(err)
	}

	stores := make([]localstore.Store, erasure.TotalShards)
	peers := make([]string, erasure.TotalShards)
	root := t.TempDir()
	for i := range stores {
		peers[i] = fmt.Sprintf("peer-%d.tailnet.local", i)
		stores[i] = localstore.New(filepath.Join(root, peers[i]))
		storedHash, err := stores[i].Put(shards[i])
		if err != nil {
			t.Fatal(err)
		}
		if storedHash != manifest.Hash(shards[i]) {
			t.Fatalf("stored hash mismatch: got %s", storedHash)
		}
	}

	m, err := manifest.New("file-1", "sample.txt", len(ciphertext), key, nonce, shards, peers)
	if err != nil {
		t.Fatal(err)
	}
	stub, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	stubPath := filepath.Join(root, "sample.txt.dstore")
	if err := os.WriteFile(stubPath, stub, 0o600); err != nil {
		t.Fatal(err)
	}

	stubBytes, err := os.ReadFile(stubPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := manifest.Unmarshal(stubBytes)
	if err != nil {
		t.Fatal(err)
	}

	recoveredShards := make([][]byte, erasure.TotalShards)
	for _, i := range []int{0, 2, 4, 5} {
		shard, err := stores[i].Get(loaded.Shards[i].Hash)
		if err != nil {
			t.Fatal(err)
		}
		recoveredShards[i] = shard
	}

	recoveredCiphertext, err := erasure.Decode(recoveredShards, loaded.CiphertextSize)
	if err != nil {
		t.Fatal(err)
	}
	recoveredPlaintext, err := cryptofile.Decrypt(recoveredCiphertext, loaded.Key, loaded.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recoveredPlaintext, plaintext) {
		t.Fatal("reconstructed plaintext mismatch")
	}
}
