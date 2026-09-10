package shardserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/peterretief/peertopeer/internal/localstore"
	"github.com/peterretief/peertopeer/internal/manifest"
	"github.com/peterretief/peertopeer/internal/protocol"
)

type Options struct {
	Identify        func(context.Context, string) (string, error)
	Members         []string
	RestrictMembers bool
	ShareID         string
	Name            string
	ReadOnly        bool
	MaxShardBytes   int64
}

// Handler is for local tests. Network servers must supply an identity resolver.
func Handler(store localstore.Store) http.Handler { return HandlerWithOptions(store, Options{}) }

func HandlerWithIdentity(store localstore.Store, identify func(context.Context, string) (string, error)) http.Handler {
	return HandlerWithOptions(store, Options{Identify: identify})
}

func HandlerWithOptions(store localstore.Store, opts Options) http.Handler {
	if opts.MaxShardBytes <= 0 {
		opts.MaxShardBytes = protocol.DefaultMaxShardBytes
	}
	members := make(map[string]bool)
	for _, name := range opts.Members {
		members[strings.ToLower(strings.TrimSpace(name))] = true
	}
	// Bound simultaneous body allocation as well as individual upload size.
	var transfer sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := ""
		if opts.Identify != nil {
			var err error
			caller, err = opts.Identify(r.Context(), r.RemoteAddr)
			if err != nil || caller == "" {
				http.Error(w, "mesh identity required", http.StatusForbidden)
				return
			}
		}
		if opts.RestrictMembers && !members[strings.ToLower(caller)] {
			http.Error(w, "device is not a sharing member", http.StatusForbidden)
			return
		}
		if opts.ShareID != "" && r.Header.Get(protocol.ShareHeader) != opts.ShareID {
			http.Error(w, "sharing group mismatch", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v1/info" && r.Method == http.MethodGet {
			used, err := store.Usage()
			if err != nil {
				http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(protocol.Info{Service: "dstore", ProtocolVersion: protocol.Version,
				Name: opts.Name, OS: runtime.GOOS, Arch: runtime.GOARCH, ShareID: opts.ShareID,
				Storage: !opts.ReadOnly, UsedBytes: used, QuotaBytes: store.Quota(), MaxShardBytes: opts.MaxShardBytes})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/shards/") {
			http.NotFound(w, r)
			return
		}
		hash := strings.TrimPrefix(r.URL.Path, "/shards/")
		if len(hash) != 64 || strings.ContainsAny(hash, "/\\") {
			http.Error(w, "invalid shard hash", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			data, err := store.Get(hash)
			if err != nil {
				storeError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
		case http.MethodPut:
			if opts.ReadOnly {
				http.Error(w, "device does not contribute storage", http.StatusForbidden)
				return
			}
			transfer.Lock()
			defer transfer.Unlock()
			r.Body = http.MaxBytesReader(w, r.Body, opts.MaxShardBytes)
			defer r.Body.Close()
			body, err := io.ReadAll(r.Body)
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "shard exceeds size limit", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "read body", http.StatusBadRequest)
				}
				return
			}
			if manifest.Hash(body) != hash {
				http.Error(w, "shard hash does not match request path", http.StatusBadRequest)
				return
			}
			if _, err := store.PutOwned(body, caller); err != nil {
				storeError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			if opts.Identify == nil {
				http.Error(w, "delete requires mesh identity", http.StatusForbidden)
				return
			}
			if err := store.DeleteOwned(hash, caller); err != nil {
				storeError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func storeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, localstore.ErrInvalidHash):
		status = http.StatusBadRequest
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, localstore.ErrQuota):
		status = http.StatusInsufficientStorage
	case errors.Is(err, localstore.ErrNotOwner):
		status = http.StatusForbidden
	}
	http.Error(w, http.StatusText(status), status)
}
