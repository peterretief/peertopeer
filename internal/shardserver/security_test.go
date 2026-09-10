package shardserver_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/protocol"
	"github.com/peterretief/peertopeer/internal/shardserver"
)

func request(handler http.Handler, method, path, identity, group string, body []byte) int {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = identity
	req.Header.Set(protocol.ShareHeader, group)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code
}

func TestIdentityMembershipAndGroupAreRequiredBeforeEveryOperation(t *testing.T) {
	store := localstore.New(t.TempDir())
	handler := shardserver.HandlerWithOptions(store, shardserver.Options{ShareID: "family", RestrictMembers: true, Members: []string{"alice"},
		Identify: func(_ context.Context, addr string) (string, error) {
			if addr == "unknown" { return "", fmt.Errorf("not in VPN") }; return addr, nil
		}})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, tc := range []struct{who, group string}{{"unknown", "family"}, {"mallory", "family"}, {"alice", "wrong"}} {
			if got := request(handler, method, "/shards/"+manifest.Hash([]byte("body")), tc.who, tc.group, []byte("body")); got != http.StatusForbidden { t.Fatalf("%s %v: %d", method, tc, got) }
		}
	}
	if got := request(handler, http.MethodGet, "/v1/info", "unknown", "family", nil); got != http.StatusForbidden { t.Fatalf("info status %d", got) }
	entries, err := os.ReadDir(store.Dir())
	if err != nil || len(entries) != 0 { t.Fatalf("unauthorized request wrote files: %v, %v", entries, err) }
}

func TestDuplicateUploadCannotTakeOwnershipAndQuotaIsEnforced(t *testing.T) {
	store := localstore.WithQuota(t.TempDir(), 4)
	handler := shardserver.HandlerWithOptions(store, shardserver.Options{MaxShardBytes: 8,
		Identify: func(_ context.Context, addr string) (string, error) { return addr, nil }})
	body := []byte("data")
	path := "/shards/"+manifest.Hash(body)
	for _, who := range []string{"alice", "bob"} {
		if got := request(handler, http.MethodPut, path, who, "", body); got != http.StatusCreated { t.Fatalf("upload %s: %d", who, got) }
	}
	if got := request(handler, http.MethodDelete, path, "bob", "", nil); got != http.StatusForbidden { t.Fatalf("ownership stolen: %d", got) }
	if got := request(handler, http.MethodPut, "/shards/"+manifest.Hash([]byte("more")), "bob", "", []byte("more")); got != http.StatusInsufficientStorage { t.Fatalf("quota status %d", got) }
	if got := request(handler, http.MethodPut, "/shards/"+manifest.Hash([]byte("too large")), "bob", "", []byte("too large")); got != http.StatusRequestEntityTooLarge { t.Fatalf("body limit status %d", got) }
	if got := request(handler, http.MethodDelete, path, "alice", "", nil); got != http.StatusNoContent { t.Fatalf("owner delete: %d", got) }
	if used, err := store.Usage(); err != nil || used != 0 { t.Fatalf("usage after delete: %d, %v", used, err) }
}

func TestCorruptUploadDoesNotPersistContent(t *testing.T) {
	store := localstore.New(t.TempDir())
	got := request(shardserver.Handler(store), http.MethodPut, "/shards/"+manifest.Hash([]byte("expected")), "", "", []byte("corrupt"))
	if got != http.StatusBadRequest { t.Fatal(got) }
	entries, err := os.ReadDir(store.Dir())
	if err != nil || len(entries) != 0 { t.Fatalf("corrupt upload persisted: %v, %v", entries, err) }
}
