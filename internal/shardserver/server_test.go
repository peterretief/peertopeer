package shardserver_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/shardserver"
)

func TestPutRejectsPathHashMismatch(t *testing.T) {
	server := httptest.NewServer(shardserver.Handler(localstore.New(t.TempDir())))
	defer server.Close()

	body := []byte("remote shard bytes")
	badHash := manifest.Hash([]byte("different bytes"))
	req, err := http.NewRequest(http.MethodPut, server.URL+"/shards/"+badHash, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
