package gradle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runs-on/action/internal/magiccache"
)

// fakeMagicCache is an in-memory GitHub Actions cache service (v2 Twirp).
// With down set, every Twirp call fails.
type fakeMagicCache struct {
	mu        sync.Mutex
	pending   map[string][]byte
	committed map[string][]byte
	calls     int
	down      bool
}

func newFakeMagicCache(t *testing.T) (*fakeMagicCache, *magiccache.Client) {
	f := &fakeMagicCache{pending: map[string][]byte{}, committed: map[string][]byte{}}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if id, ok := strings.CutPrefix(r.URL.Path, "/blob/"); ok {
			if r.Method == http.MethodPut {
				f.pending[id], _ = io.ReadAll(r.Body)
				return
			}
			_, _ = w.Write(f.committed[id])
			return
		}
		f.calls++
		if f.down {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := fmt.Sprint(req["version"], "/", req["key"])
		var resp any
		switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
		case "CreateCacheEntry":
			if _, ok := f.committed[id]; ok {
				w.WriteHeader(http.StatusConflict)
				resp = map[string]string{"code": "already_exists"}
				break
			}
			resp = map[string]any{"ok": true, "signedUploadUrl": srv.URL + "/blob/" + id}
		case "FinalizeCacheEntryUpload":
			f.committed[id] = f.pending[id]
			resp = map[string]any{"ok": true}
		case "GetCacheEntryDownloadURL":
			_, ok := f.committed[id]
			resp = map[string]any{"ok": ok, "signedDownloadUrl": srv.URL + "/blob/" + id}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return f, magiccache.NewClient(srv.URL, "job-token")
}

func (f *fakeMagicCache) set(down bool) (calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
	return f.calls
}

func newTestServer(t *testing.T, remote *magiccache.Client) (*Server, *httptest.Server) {
	s, err := NewServer(t.TempDir(), remote, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func do(t *testing.T, method, url string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// What one job stores, a job on another runner, with nothing on its disk,
// loads from Magic Cache.
func TestAnotherJobLoadsWhatAJobStored(t *testing.T) {
	_, remote := newFakeMagicCache(t)
	first, firstURL := newTestServer(t, remote)
	entry := bytes.Repeat([]byte("task output "), 1000)

	if status, _ := do(t, http.MethodPut, firstURL.URL+"/cache/0123abcd", entry); status != http.StatusCreated {
		t.Fatalf("store answered %d", status)
	}
	if status, got := do(t, http.MethodGet, firstURL.URL+"/cache/0123abcd", nil); status != http.StatusOK || !bytes.Equal(got, entry) {
		t.Fatalf("load in the same job answered %d with %d bytes", status, len(got))
	}
	stats := first.Stop(time.Minute)
	if stats.Stores != 1 || stats.Uploaded != 1 || stats.LocalHits != 1 || stats.Pending != 0 {
		t.Fatalf("first job: %+v", stats)
	}

	second, secondURL := newTestServer(t, remote)
	if status, got := do(t, http.MethodGet, secondURL.URL+"/cache/0123abcd", nil); status != http.StatusOK || !bytes.Equal(got, entry) {
		t.Fatalf("load in another job answered %d with %d bytes", status, len(got))
	}
	if status, _ := do(t, http.MethodGet, secondURL.URL+"/cache/0123abce", nil); status != http.StatusNotFound {
		t.Fatalf("load of an entry no job stored answered %d", status)
	}
	// Storing an entry another job uploaded costs one call, not an error.
	if status, _ := do(t, http.MethodPut, secondURL.URL+"/cache/0123abcd", entry); status != http.StatusCreated {
		t.Fatalf("store answered %d", status)
	}
	stats = second.Stop(time.Minute)
	if stats.RemoteHits != 1 || stats.Misses != 1 || stats.DownloadedBytes != int64(len(entry)) || stats.AlreadyStored != 1 || stats.RemoteErrors != 0 {
		t.Fatalf("second job: %+v", stats)
	}
}

// A Magic Cache that fails makes loads misses and keeps stores on local
// disk, and after a few failures the server stops asking it.
func TestFailingMagicCacheDoesNotFailTheBuild(t *testing.T) {
	fake, remote := newFakeMagicCache(t)
	s, srv := newTestServer(t, remote)
	fake.set(true)
	for i := range 2 * remoteFailureLimit {
		if status, _ := do(t, http.MethodGet, fmt.Sprintf("%s/cache/%x", srv.URL, i+10), nil); status != http.StatusNotFound {
			t.Fatalf("load answered %d, want a miss", status)
		}
	}
	if calls := fake.set(false); calls != remoteFailureLimit {
		t.Errorf("Magic Cache got %d calls, want %d before the server stops asking", calls, remoteFailureLimit)
	}
	if status, _ := do(t, http.MethodPut, srv.URL+"/cache/ff", []byte("output")); status != http.StatusCreated {
		t.Fatalf("store answered %d", status)
	}
	if status, got := do(t, http.MethodGet, srv.URL+"/cache/ff", nil); status != http.StatusOK || string(got) != "output" {
		t.Fatalf("load of a stored entry answered %d %q", status, got)
	}
	if stats := s.Stop(time.Minute); stats.RemoteErrors != remoteFailureLimit || stats.Uploaded != 0 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestRejectsWhatGradleNeverSends(t *testing.T) {
	_, srv := newTestServer(t, nil)
	for _, path := range []string{"/cache/", "/cache/..%2fstate.json", "/cache/ABCD", "/cache/" + strings.Repeat("a", 129)} {
		if status, _ := do(t, http.MethodPut, srv.URL+path, []byte("x")); status != http.StatusBadRequest {
			t.Errorf("PUT %s answered %d", path, status)
		}
	}
	s, _ := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodPut, "/cache/abcd", strings.NewReader("x"))
	req.ContentLength = maxEntrySize + 1
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize PUT answered %d", rec.Code)
	}
}
