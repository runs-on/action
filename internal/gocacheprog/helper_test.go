package gocacheprog_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCacheService is an in-memory GitHub Actions cache service (v2 Twirp):
// immutable entries, created then uploaded then finalized.
type fakeCacheService struct {
	mu        sync.Mutex
	pending   map[string][]byte
	committed map[string][]byte
	order     []string // committed ids, oldest first
	downloads int
	creates   int // CreateCacheEntry calls for outputs, rejected ones too
	testLogs  int // uploaded entries holding a test's input record
}

// entries counts committed entries, build outputs and manifests apart.
func (f *fakeCacheService) entries() (outputs, manifests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.committed {
		if strings.Contains(id, "-manifest") {
			manifests++
		} else {
			outputs++
		}
	}
	return outputs, manifests
}

func (f *fakeCacheService) testLogCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.testLogs
}

func (f *fakeCacheService) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func (f *fakeCacheService) downloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}

func newFakeCacheService() (*httptest.Server, *fakeCacheService) {
	f := &fakeCacheService{pending: map[string][]byte{}, committed: map[string][]byte{}}
	var srv *httptest.Server
	mux := http.NewServeMux()
	twirp := func(method string, handle func(req map[string]any) (int, any)) {
		mux.HandleFunc("/twirp/github.actions.results.api.v1.CacheService/"+method, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
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
		if !strings.Contains(fmt.Sprint(req["key"]), "-manifest") {
			f.creates++
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
		// Restore keys match by prefix, newest entry first.
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
			// An entry is a 4-byte magic, a 32-byte output ID, then the output.
			// The compiled testing/internal/testdeps package holds the same
			// string further in.
			if body := f.pending[key]; len(body) > 36 && bytes.HasPrefix(body[36:], []byte("# test log\n")) {
				f.testLogs++
			}
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
	return srv, f
}

// TestGoCommandSharesBuildsButNotTestResults drives the real go command
// through the helper against an in-memory cache service. Each build or test
// run uses its own home directory, as another job would.
func TestGoCommandSharesBuildsButNotTestResults(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not found")
	}
	tmp := t.TempDir()
	helper := filepath.Join(tmp, "helper")
	if out, err := exec.Command(goBin, "build", "-o", helper, "./testdata/helper").CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	module := filepath.Join(tmp, "mod")
	files := map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.24\n",
		"main.go":         "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/m/lib\"\n)\n\nfunc main() { fmt.Println(lib.Answer()) }\n",
		"lib/lib.go":      "package lib\n\nfunc Answer() int { return 42 }\n",
		"lib/lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(Answer())\n\t}\n}\n",
	}
	// The go command does not cache its module index for files modified in
	// the last few seconds, so fresh files would make the cold and warm
	// builds look up different entries.
	old := time.Now().Add(-time.Hour)
	for name, content := range files {
		path := filepath.Join(module, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	srv, service := newFakeCacheService()
	defer srv.Close()

	goCmd := func(job, resultsURL string, args ...string) string {
		t.Helper()
		home := filepath.Join(tmp, job)
		cmd := exec.Command(goBin, args...)
		cmd.Dir = module
		cmd.Env = append(os.Environ(),
			"HOME="+home,
			"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
			"GOCACHEPROG="+helper,
			"GOCACHE="+filepath.Join(home, "gocache"),
			"GOFLAGS=",
			"GOTOOLCHAIN=local",
			"ACTIONS_RESULTS_URL="+resultsURL,
			"ACTIONS_RUNTIME_TOKEN=test-token",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	// The go command re-links once for a new output path, so keep it.
	app := filepath.Join(tmp, "app")

	goCmd("cold", srv.URL, "build", "-o", app, ".")
	coldOutputs, coldManifests := service.entries()
	if coldOutputs == 0 || coldManifests != 1 {
		t.Fatalf("cold build uploaded %d outputs and %d manifests, want outputs and 1 manifest", coldOutputs, coldManifests)
	}

	createsBefore := service.createCount()
	goCmd("warm", srv.URL, "build", "-o", app, ".")
	if got := service.createCount() - createsBefore; got != 0 {
		t.Errorf("a build with an empty cache directory tried to upload %d outputs, want 0: it compiled something", got)
	}
	if service.downloadCount() < coldOutputs {
		t.Errorf("warm build downloaded %d entries, want at least the %d the cold build stored", service.downloadCount(), coldOutputs)
	}
	if out, err := exec.Command(app).Output(); err != nil || string(out) != "42\n" {
		t.Errorf("binary linked from remote outputs printed %q, %v", out, err)
	}

	// A cache service that is down degrades to a local build.
	goCmd("down", "http://127.0.0.1:1", "build", "-o", app, ".")

	// Test results stay in the job that produced them: another job runs its
	// tests, while a repeat in the same job is cached as usual.
	goCmd("test-job-1", srv.URL, "test", "./...")
	if out := goCmd("test-job-2", srv.URL, "test", "./..."); strings.Contains(out, "(cached)") {
		t.Errorf("a job reused another job's test result:\n%s", out)
	}
	if out := goCmd("test-job-2", srv.URL, "test", "./..."); !strings.Contains(out, "(cached)") {
		t.Errorf("a repeat in the same job reran its tests:\n%s", out)
	}
	if n := service.testLogCount(); n != 0 {
		t.Errorf("%d test input records reached the cache service, want 0", n)
	}
}
