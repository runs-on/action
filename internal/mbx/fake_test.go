package mbx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testRuntimeToken = "test-token"

// fakeCacheService is an in-memory GitHub Actions cache service (v2 Twirp):
// immutable entries, created then uploaded then finalized; restore keys match
// the newest entry by prefix.
type fakeCacheService struct {
	mu        sync.Mutex
	pending   map[string][]byte
	committed map[string][]byte
	order     []string // committed ids, oldest first
	downloads int
	// readOnly refuses writes, as for a job whose token only reads.
	readOnly bool
}

func newFakeCacheService(t *testing.T) (*httptest.Server, *fakeCacheService) {
	t.Helper()
	f := &fakeCacheService{pending: map[string][]byte{}, committed: map[string][]byte{}}
	var srv *httptest.Server
	mux := http.NewServeMux()
	twirp := func(method string, handle func(req map[string]any) (int, any)) {
		mux.HandleFunc("/twirp/github.actions.results.api.v1.CacheService/"+method, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testRuntimeToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			status, resp := handle(req)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		})
	}
	id := func(req map[string]any) string { return fmt.Sprint(req["version"], "/", req["key"]) }
	twirp("CreateCacheEntry", func(req map[string]any) (int, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.readOnly {
			return http.StatusForbidden, map[string]string{"code": "permission_denied", "msg": "read-only token"}
		}
		if _, ok := f.committed[id(req)]; ok {
			return http.StatusConflict, map[string]string{"code": "already_exists", "msg": "exists"}
		}
		f.pending[id(req)] = nil
		return http.StatusOK, map[string]any{"ok": true, "signedUploadUrl": srv.URL + "/blob/" + id(req)}
	})
	twirp("FinalizeCacheEntryUpload", func(req map[string]any) (int, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		data, ok := f.pending[id(req)]
		if !ok || fmt.Sprint(len(data)) != req["size_bytes"] {
			return http.StatusOK, map[string]any{"ok": false}
		}
		f.committed[id(req)] = data
		f.order = append(f.order, id(req))
		delete(f.pending, id(req))
		return http.StatusOK, map[string]any{"ok": true}
	})
	twirp("GetCacheEntryDownloadURL", func(req map[string]any) (int, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.committed[id(req)]; ok {
			return http.StatusOK, map[string]any{"ok": true, "matchedKey": req["key"], "signedDownloadUrl": srv.URL + "/blob/" + id(req)}
		}
		restoreKeys, _ := req["restore_keys"].([]any)
		for _, restoreKey := range restoreKeys {
			prefix := fmt.Sprint(req["version"], "/", restoreKey)
			for i := len(f.order) - 1; i >= 0; i-- {
				if strings.HasPrefix(f.order[i], prefix) {
					key := strings.TrimPrefix(f.order[i], fmt.Sprint(req["version"], "/"))
					return http.StatusOK, map[string]any{"ok": true, "matchedKey": key, "signedDownloadUrl": srv.URL + "/blob/" + f.order[i]}
				}
			}
		}
		return http.StatusOK, map[string]any{"ok": false}
	})
	mux.HandleFunc("/blob/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/blob/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, ok := f.pending[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.pending[key], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			data, ok := f.committed[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.downloads++
			_, _ = w.Write(data)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeCacheService) setReadOnly(readOnly bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readOnly = readOnly
}

// versions lists the cache versions that hold entries.
func (f *fakeCacheService) versions() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	versions := map[string]int{}
	for id := range f.committed {
		version, _, _ := strings.Cut(id, "/")
		versions[version]++
	}
	return versions
}

func (f *fakeCacheService) entryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.committed)
}

func (f *fakeCacheService) downloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}
