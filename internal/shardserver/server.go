package shardserver

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/peterretief/peertopeer/internal/localstore"
)

func Handler(store localstore.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shards/{hash}", func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("hash")
		if strings.Contains(hash, "/") || strings.Contains(hash, "\\") {
			http.Error(w, "invalid shard hash", http.StatusBadRequest)
			return
		}
		data, err := store.Get(hash)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, localstore.ErrInvalidHash):
				status = http.StatusBadRequest
			case errors.Is(err, os.ErrNotExist):
				status = http.StatusNotFound
			}
			http.Error(w, "shard not found", status)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	})
	return mux
}
