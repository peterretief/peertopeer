package dstore_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/shardserver"
)

func TestProcessDirectoryCreatesPortableManifestAndHTTPRestoreRecreatesOriginal(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "outfiles")
	shards := filepath.Join(root, "shards")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(shardserver.Handler(localstore.New(shards)))
	defer server.Close()

	original := filepath.Join(origin, "hello.txt")
	plaintext := bytes.Repeat([]byte("hello from outfiles\n"), 512)
	if err := os.WriteFile(original, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := dstore.ProcessDirectory(dstore.Config{OriginDir: origin, ShardDir: shards, BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("processed files: got %d want 1", len(results))
	}
	if _, err := os.Stat(filepath.Join(origin, ".gitkeep.dstore")); !os.IsNotExist(err) {
		t.Fatalf("hidden files should not be processed, stat err=%v", err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("original should be removed after manifest write, stat err=%v", err)
	}

	stubPath := original + dstore.StubExtension
	stubBytes, err := os.ReadFile(stubPath)
	if err != nil {
		t.Fatalf("stub not created: %v", err)
	}
	loaded, err := manifest.Unmarshal(stubBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range loaded.Shards {
		if ref.URL == "" {
			t.Fatalf("shard %d has no portable URL", i)
		}
		if !strings.HasPrefix(ref.URL, server.URL+"/shards/") {
			t.Fatalf("shard %d URL %q does not use shard server URL", i, ref.URL)
		}
	}

	restored, err := dstore.RestoreFile(stubPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if restored != original {
		t.Fatalf("restored path: got %s want %s", restored, original)
	}
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("restored file mismatch")
	}
}
