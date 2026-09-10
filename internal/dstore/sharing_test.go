package dstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/peterretief/peertopeer/internal/dstore"
	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/peer"
	"github.com/peterretief/peertopeer/internal/shardserver"
)

func sharingCluster(t *testing.T) (dstore.Config, []*httptest.Server) {
	t.Helper()
	cfg := dstore.Config{ShardDir: filepath.Join(t.TempDir(), "local"), ShareID: "family", MinPeers: 3,
		RequireCapabilities: true, PeerPorts: make(map[string]string)}
	var servers []*httptest.Server
	var candidates []peer.Peer
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("node-%d", i)
		handler := shardserver.HandlerWithOptions(localstore.WithQuota(t.TempDir(), 1<<20), shardserver.Options{
			Name: name, ShareID: cfg.ShareID, Members: []string{"owner"}, RestrictMembers: true,
			Identify: func(context.Context, string) (string, error) { return "owner", nil },
		})
		srv := httptest.NewUnstartedServer(handler)
		srv.Listener.Close()
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.%d:0", i+2))
		if err != nil { t.Fatal(err) }
		srv.Listener = listener
		srv.Start()
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil { t.Fatal(err) }
		candidates = append(candidates, peer.Peer{HostName: name, TailIP: u.Hostname(), Online: true})
		cfg.PeerPorts[name] = u.Port()
		servers = append(servers, srv)
	}
	cfg.Discover = func(context.Context) ([]peer.Peer, error) { return candidates, nil }
	return cfg, servers
}

func TestAddDirectoryAndRestoreWithOneStorageDeviceOffline(t *testing.T) {
	cfg, servers := sharingCluster(t)
	root := t.TempDir()
	source := filepath.Join(root, "photos")
	for _, dir := range []string{"raw", "empty", ".private"} {
		if err := os.MkdirAll(filepath.Join(source, dir), 0o700); err != nil { t.Fatal(err) }
	}
	files := map[string][]byte{"a.jpg": bytes.Repeat([]byte("image data"), 81), "raw/b.txt": []byte("nested"), ".private/notes": []byte("hidden input"), "zero": {}}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(source, rel), body, 0o600); err != nil { t.Fatal(err) }
	}
	library := filepath.Join(root, "library")
	results, err := dstore.AddPath(context.Background(), source, library, cfg)
	if err != nil { t.Fatal(err) }
	if len(results) != len(files) { t.Fatalf("added %d files, want %d", len(results), len(files)) }
	for _, result := range results {
		if _, err := os.Stat(result.OriginalPath); err != nil { t.Fatalf("source removed: %v", err) }
		seen := map[string]bool{}
		for _, ref := range result.Manifest.Shards { seen[ref.Peer] = true }
		if len(seen) != 3 { t.Fatalf("placement needs three devices: %v", seen) }
	}
	servers[1].Close()
	output := filepath.Join(root, "restored")
	paths, err := dstore.RestoreDirectory(context.Background(), filepath.Join(library, "photos"), output, "")
	if err != nil { t.Fatal(err) }
	if len(paths) != len(files) { t.Fatalf("restored %d files", len(paths)) }
	for rel, body := range files {
		for _, dir := range []string{source, output} {
			got, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil || !bytes.Equal(got, body) { t.Fatalf("%s/%s: %q, %v", dir, rel, got, err) }
		}
	}
	if st, err := os.Stat(filepath.Join(output, "empty")); err != nil || !st.IsDir() { t.Fatalf("empty directory lost: %v", err) }
	if _, err := dstore.RestoreFile(context.Background(), results[0].StubPath, "", results[0].OriginalPath); !errors.Is(err, os.ErrExist) { t.Fatalf("restore must not overwrite source: %v", err) }
	if _, err := dstore.AddPath(context.Background(), source, library, cfg); !errors.Is(err, os.ErrExist) { t.Fatalf("add must not overwrite existing library: %v", err) }
}

func TestUnavailableDiscoveryRetainsSourceAndDoesNotPublishStub(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "original")
	if err := os.WriteFile(source, []byte("keep me"), 0o600); err != nil { t.Fatal(err) }
	cfg := dstore.Config{ShardDir: filepath.Join(root, "shards"), Discover: func(context.Context) ([]peer.Peer, error) { return nil, fmt.Errorf("VPN unavailable") }}
	if _, err := dstore.ProcessFileWithConfig(context.Background(), source, cfg); err == nil { t.Fatal("expected placement failure") }
	if _, err := os.Stat(source); err != nil { t.Fatal(err) }
	if _, err := os.Stat(source+dstore.StubExtension); !errors.Is(err, os.ErrNotExist) { t.Fatalf("manifest published: %v", err) }
}

func TestAddRejectsOversizeSourceAndSymlinks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil { t.Fatal(err) }
	file := filepath.Join(source, "big")
	if err := os.WriteFile(file, []byte("too big"), 0o600); err != nil { t.Fatal(err) }
	cfg := dstore.Config{ShardDir: filepath.Join(root, "shards"), MaxFileBytes: 2}
	if _, err := dstore.AddPath(context.Background(), file, filepath.Join(root, "library"), cfg); err == nil { t.Fatal("oversize file accepted") }
	if err := os.Symlink(file, filepath.Join(source, "a-link")); err != nil { t.Fatal(err) }
	if _, err := dstore.AddPath(context.Background(), source, filepath.Join(root, "library2"), cfg); err == nil { t.Fatal("symlink silently accepted") }
}

func TestWatchRetriesFailedPeerAndStopsOnCancellation(t *testing.T) {
	var ready atomic.Bool
	handler := shardserver.Handler(localstore.New(t.TempDir()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() { http.Error(w, "offline", http.StatusServiceUnavailable); return }
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	root := t.TempDir()
	origin := filepath.Join(root, "inbox")
	if err := os.Mkdir(origin, 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(origin, "input"), []byte("retry later"), 0o600); err != nil { t.Fatal(err) }
	pending := make(chan error, 1)
	processed := make(chan dstore.ProcessResult, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := dstore.Config{OriginDir: origin, ShardDir: filepath.Join(root, "shards"), KeepOriginal: true,
		Peers: []peer.Peer{{HostName: "storage", TailIP: u.Hostname(), Online: true}}, PeerPorts: map[string]string{"storage": u.Port()},
		OnError: func(err error) { select { case pending <- err: default: } }}
	go func() { done <- dstore.Watch(ctx, cfg, 10*time.Millisecond, func(result dstore.ProcessResult) { processed <- result }) }()
	select { case <-pending: case <-time.After(3*time.Second): t.Fatal("watcher did not report failure") }
	ready.Store(true)
	select { case <-processed: case <-time.After(3*time.Second): t.Fatal("watcher did not retry") }
	cancel()
	select { case err := <-done: if !errors.Is(err, context.Canceled) { t.Fatal(err) }; case <-time.After(time.Second): t.Fatal("watcher did not stop") }
}
