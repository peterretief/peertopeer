package dstore_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/peer"
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
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(origin, "hello.txt")
	plaintext := bytes.Repeat([]byte("hello from outfiles\n"), 512)
	if err := os.WriteFile(original, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	results, err := dstore.ProcessDirectory(ctx, dstore.Config{
		OriginDir:  origin,
		ShardDir:   shards,
		BaseURL:    server.URL,
		ListenPort: "1",
		PeerPorts:  map[string]string{"test-shard-node": serverURL.Port()},
		Peers: []peer.Peer{{
			HostName: "test-shard-node",
			TailIP:   serverURL.Hostname(),
			Online:   true,
		}},
	})
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

	restored, err := dstore.RestoreFile(ctx, stubPath, "", "")
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

func TestProcessDirectoryRecursesIntoNestedDirectories(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "outfiles")
	shards := filepath.Join(root, "shards")
	if err := os.MkdirAll(filepath.Join(origin, "photos", "raw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(origin, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		filepath.Join(origin, "top.txt"):                []byte("top-level file"),
		filepath.Join(origin, "photos", "raw", "a.txt"): []byte("nested file"),
	}
	for path, body := range files {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(origin, ".hidden", "skip.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "already.txt.dstore"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(shardserver.Handler(localstore.New(shards)))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	results, err := dstore.ProcessDirectory(context.Background(), dstore.Config{
		OriginDir:  origin,
		ShardDir:   shards,
		ListenPort: "1",
		PeerPorts:  map[string]string{"test-shard-node": serverURL.Port()},
		Peers: []peer.Peer{{
			HostName: "test-shard-node",
			TailIP:   serverURL.Hostname(),
			Online:   true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(files) {
		t.Fatalf("processed files: got %d want %d", len(results), len(files))
	}
	for original, body := range files {
		if _, err := os.Stat(original); !os.IsNotExist(err) {
			t.Fatalf("original %s should be removed, stat err=%v", original, err)
		}
		stub := original + dstore.StubExtension
		if _, err := os.Stat(stub); err != nil {
			t.Fatalf("stub %s not created: %v", stub, err)
		}
		restored, err := dstore.RestoreFile(context.Background(), stub, shards, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("restored %s mismatch", restored)
		}
	}
	if _, err := os.Stat(filepath.Join(origin, ".hidden", "skip.txt.dstore")); !os.IsNotExist(err) {
		t.Fatalf("hidden nested files should not be processed, stat err=%v", err)
	}
}

func TestProcessFileWithConfiguredPeersPushesShardURLs(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "outfiles")
	localShards := filepath.Join(root, "local-shards")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}

	var peers []peer.Peer
	peerPorts := make(map[string]string)
	remoteStores := make([]localstore.Store, 2)
	for i := range remoteStores {
		name := "peer-" + string(rune('a'+i))
		remoteStores[i] = localstore.New(filepath.Join(root, name))
		server := httptest.NewServer(shardserver.Handler(remoteStores[i]))
		defer server.Close()

		serverURL, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peer.Peer{HostName: name, TailIP: serverURL.Hostname(), Online: true})
		peerPorts[name] = serverURL.Port()
	}

	original := filepath.Join(origin, "configured.txt")
	plaintext := bytes.Repeat([]byte("configured peer shard target\n"), 256)
	if err := os.WriteFile(original, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := dstore.ProcessFileWithConfig(context.Background(), original, dstore.Config{
		ShardDir:   localShards,
		ListenPort: "1",
		PeerPorts:  peerPorts,
		Peers:      peers,
	})
	if err != nil {
		t.Fatal(err)
	}

	for i, ref := range result.Manifest.Shards {
		wantPeer := peers[i%len(peers)].HostName
		if ref.Peer != wantPeer {
			t.Fatalf("shard %d peer: got %q want %q", i, ref.Peer, wantPeer)
		}
		if !strings.Contains(ref.URL, peerPorts[wantPeer]) {
			t.Fatalf("shard %d URL %q does not use configured peer port %s", i, ref.URL, peerPorts[wantPeer])
		}
		if _, err := remoteStores[i%len(remoteStores)].Get(ref.Hash); err != nil {
			t.Fatalf("remote shard %d not stored on %s: %v", i, wantPeer, err)
		}
	}

	restored, err := dstore.RestoreFile(context.Background(), result.StubPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("restored file mismatch")
	}
}

func TestProcessFileWithConfiguredPeerFailureKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "outfiles")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(origin, "offline.txt")
	plaintext := []byte("do not remove me until the remote shard is real")
	if err := os.WriteFile(original, plaintext, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := dstore.ProcessFileWithConfig(context.Background(), original, dstore.Config{
		ShardDir:   filepath.Join(root, "local-shards"),
		ListenPort: "1",
		PeerPorts:  map[string]string{"offline-peer": "1"},
		Peers: []peer.Peer{{
			HostName: "offline-peer",
			TailIP:   "127.0.0.1",
			Online:   true,
		}},
	})
	if err == nil {
		t.Fatal("expected configured peer failure")
	}
	if !strings.Contains(err.Error(), "configured peer offline-peer") {
		t.Fatalf("error %q does not identify configured peer failure", err)
	}
	got, err := os.ReadFile(original)
	if err != nil {
		t.Fatalf("original should remain after failed remote push: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("original changed after failed remote push")
	}
	if _, err := os.Stat(original + dstore.StubExtension); !os.IsNotExist(err) {
		t.Fatalf("stub should not be written after failed remote push, stat err=%v", err)
	}
}
