package gocacheprog_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runs-on/action/internal/gocacheprog"
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
	rejected  int // calls with another token, answered 401
	// refuseDownloads answers downloads as Magic Cache does when another job
	// replaced the entry since the lookup.
	refuseDownloads bool
	testData        int // uploaded entries holding what a test read or printed
}

// isList tells a job's list from a build output by its cache version.
func isList(id string) bool { return strings.Contains(id, "-lists/") }

// entries counts committed entries, build outputs and job lists apart.
func (f *fakeCacheService) entries() (outputs, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.committed {
		if isList(id) {
			lists++
		} else {
			outputs++
		}
	}
	return outputs, lists
}

// testOutputMarker is what the module's test prints. It reaches the test
// through its environment, so no compiled package holds it.
const testOutputMarker = "printed-by-the-test"

func (f *fakeCacheService) testDataCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.testData
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
				f.mu.Lock()
				f.rejected++
				f.mu.Unlock()
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
		if !isList(id(req)) {
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
			// An entry is a 44-byte header, then the output. The compiled
			// testing/internal/testdeps package holds "# test log" further in.
			if body := f.pending[key]; len(body) > 44 && (bytes.HasPrefix(body[44:], []byte("# test log\n")) || bytes.HasPrefix(body[44:], []byte("mode: ")) || bytes.Contains(body, []byte(testOutputMarker))) {
				f.testData++
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			data, ok := f.committed[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.refuseDownloads {
				w.WriteHeader(http.StatusPreconditionFailed)
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
// run uses its own home and temporary directory, as another job would.
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
		"lib/lib_test.go": "package lib\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestAnswer(t *testing.T) {\n\tfmt.Println(os.Getenv(\"TEST_OUTPUT\"))\n\tif Answer() != 42 {\n\t\tt.Fatal(Answer())\n\t}\n}\n",
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

	shareTests := false
	goCmd := func(job, resultsURL string, args ...string) string {
		t.Helper()
		home := filepath.Join(tmp, job)
		prog := helper
		if shareTests {
			prog += " --share-test-results"
		}
		// As in a job: the action kept the token in a file for the helper,
		// and run steps do not have it in their environment.
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RUNNER_TEMP", home)
		if err := gocacheprog.StoreToken("test-token"); err != nil {
			t.Fatal(err)
		}
		stepEnv := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "ACTIONS_RUNTIME_TOKEN=") })
		cmd := exec.Command(goBin, args...)
		cmd.Dir = module
		cmd.Env = append(stepEnv,
			"HOME="+home,
			"RUNNER_TEMP="+home,
			"GOCACHEPROG="+prog,
			"TEST_OUTPUT="+testOutputMarker,
			"GOCACHE="+filepath.Join(home, "gocache"),
			"GOFLAGS=",
			"GOTOOLCHAIN=local",
			"ACTIONS_RESULTS_URL="+resultsURL,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	// The go command re-links once for a new output path, so keep it.
	app := filepath.Join(tmp, "app")

	// What the action does around a job's steps: when the job starts, it
	// prefetches what the job used last time; when it ends, it saves what
	// the job used.
	const buildJob = "list-of-the-build-job"
	options := func(job string) gocacheprog.Options {
		t.Setenv("RUNNER_TEMP", filepath.Join(tmp, job))
		t.Setenv("ACTIONS_RESULTS_URL", srv.URL)
		t.Setenv("ACTIONS_RUNTIME_TOKEN", "test-token")
		return gocacheprog.OptionsFromEnv(false)
	}
	startJob := func(job string) {
		t.Helper()
		if err := gocacheprog.Prefetch(options(job), buildJob); err != nil {
			t.Fatal(err)
		}
	}
	endJob := func(job string) int {
		t.Helper()
		saved, err := gocacheprog.SaveList(options(job), buildJob)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	startJob("cold")
	goCmd("cold", srv.URL, "build", "-o", app, ".")
	if saved := endJob("cold"); saved == 0 {
		t.Fatal("a job that compiled its packages saved no list of what it used")
	}
	coldOutputs, lists := service.entries()
	if coldOutputs == 0 || lists != 1 {
		t.Fatalf("the cold job left %d outputs and %d lists, want outputs and 1 list", coldOutputs, lists)
	}

	// The job's next run, on another runner: the prefetch brings everything
	// before the go command asks.
	startJob("warm")
	createsBefore, downloadsBefore := service.createCount(), service.downloadCount()
	goCmd("warm", srv.URL, "build", "-o", app, ".")
	if got := service.createCount() - createsBefore; got != 0 {
		t.Errorf("a build after its job's prefetch tried to upload %d outputs, want 0: it compiled something", got)
	}
	if got := service.downloadCount() - downloadsBefore; got != 0 {
		t.Errorf("the go command downloaded %d entries its job's prefetch did not bring, want 0", got)
	}
	if saved := endJob("warm"); saved != 0 {
		t.Errorf("a job that used what it prefetched saved a list of %d entries, want none", saved)
	}
	if out, err := exec.Command(app).Output(); err != nil || string(out) != "42\n" {
		t.Errorf("binary linked from remote outputs printed %q, %v", out, err)
	}

	// A cache service that is down degrades to a local build.
	goCmd("down", "http://127.0.0.1:1", "build", "-o", app, ".")

	// The go command stores three records for this test run: what the test
	// read, what it printed, and its coverage profile.
	testArgs := []string{"test", "-v", "-coverprofile=" + filepath.Join(tmp, "cover.out"), "./..."}

	// Test results stay in the job that produced them: another job runs its
	// tests, while a repeat in the same job is cached as usual.
	goCmd("test-job-1", srv.URL, testArgs...)
	if out := goCmd("test-job-2", srv.URL, testArgs...); strings.Contains(out, "(cached)") {
		t.Errorf("a job reused another job's test result:\n%s", out)
	}
	if out := goCmd("test-job-2", srv.URL, testArgs...); !strings.Contains(out, "(cached)") {
		t.Errorf("a repeat in the same job reran its tests:\n%s", out)
	}
	if n := service.testDataCount(); n != 0 {
		t.Errorf("%d records of what a test read, printed or covered reached the cache service, want 0", n)
	}

	// Jobs that opt in share test results with each other, and only with
	// each other.
	shareTests = true
	goCmd("shared-job-1", srv.URL, testArgs...)
	if out := goCmd("shared-job-2", srv.URL, testArgs...); !strings.Contains(out, "(cached)") {
		t.Errorf("a job that shares test results reran another's tests:\n%s", out)
	}
	// go clean -testcache expires the results stored before it, shared ones
	// included.
	if err := os.MkdirAll(filepath.Join(tmp, "shared-job-3", "gocache"), 0o755); err != nil {
		t.Fatal(err)
	}
	goCmd("shared-job-3", srv.URL, "clean", "-testcache")
	if out := goCmd("shared-job-3", srv.URL, testArgs...); strings.Contains(out, "(cached)") {
		t.Errorf("a shared test result outlived go clean -testcache:\n%s", out)
	}

	shareTests = false
	if out := goCmd("test-job-3", srv.URL, testArgs...); strings.Contains(out, "(cached)") {
		t.Errorf("a job that did not opt in reused a shared test result:\n%s", out)
	}
}

// TestEntryReplacedDuringDownloadIsAMiss covers two jobs writing the same
// entries at once: Magic Cache refuses the download of an entry replaced
// since its lookup. That is a miss, and the outputs compiled after it are
// still uploaded.
func TestEntryReplacedDuringDownloadIsAMiss(t *testing.T) {
	srv, service := newFakeCacheService()
	defer srv.Close()
	open := func() *gocacheprog.Cache {
		c, err := gocacheprog.New(gocacheprog.Options{Dir: t.TempDir(), ResultsURL: srv.URL, Token: "test-token", Namespace: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	put := func(c *gocacheprog.Cache, action byte, output string) {
		t.Helper()
		outputID := sha256.Sum256([]byte(output))
		if _, err := c.Put(bytes.Repeat([]byte{action}, 32), outputID[:], []byte(output)); err != nil {
			t.Fatal(err)
		}
	}

	first := open()
	put(first, 1, "one")
	_ = first.Close()

	service.mu.Lock()
	service.refuseDownloads = true
	service.mu.Unlock()
	second := open()
	if _, ok, err := second.Get(bytes.Repeat([]byte{1}, 32)); ok || err != nil {
		t.Fatalf("a refused download returned %v, %v, want a miss", ok, err)
	}
	put(second, 2, "two")
	_ = second.Close()

	if outputs, _ := service.entries(); outputs != 2 {
		t.Errorf("the cache service holds %d outputs, want the 2 that were put", outputs)
	}
}

// TestRejectedTokenIsNotAskedAgain: a cache that rejects the job's token will
// reject every request of the build, so the go command stops asking after the
// first one instead of paying for each lookup and upload.
func TestRejectedTokenIsNotAskedAgain(t *testing.T) {
	srv, service := newFakeCacheService()
	defer srv.Close()
	c, err := gocacheprog.New(gocacheprog.Options{Dir: t.TempDir(), ResultsURL: srv.URL, Token: "expired-token", Namespace: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for action := byte(1); action <= 3; action++ {
		if _, ok, err := c.Get(bytes.Repeat([]byte{action}, 32)); ok || err != nil {
			t.Fatalf("a lookup with a rejected token returned %v, %v, want a miss", ok, err)
		}
	}
	output := sha256.Sum256([]byte("out"))
	if _, err := c.Put(bytes.Repeat([]byte{4}, 32), output[:], []byte("out")); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.rejected != 1 {
		t.Errorf("the cache service rejected %d requests, want 1: the first", service.rejected)
	}
}

// TestJobListHoldsOnlySharedEntries: a test record kept on local disk is not
// in the remote cache, so the job's next run has nothing to prefetch for it,
// even after a later go command of the same job read it back.
func TestJobListHoldsOnlySharedEntries(t *testing.T) {
	srv, service := newFakeCacheService()
	defer srv.Close()
	job := gocacheprog.Options{Dir: t.TempDir(), ResultsURL: srv.URL, Token: "test-token", Namespace: "test"}
	open := func() *gocacheprog.Cache {
		c, err := gocacheprog.New(job)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	id := func(action byte) []byte { return bytes.Repeat([]byte{action}, 32) }
	put := func(c *gocacheprog.Cache, action byte, output string) {
		t.Helper()
		outputID := sha256.Sum256([]byte(output))
		if _, err := c.Put(id(action), outputID[:], []byte(output)); err != nil {
			t.Fatal(err)
		}
	}

	first := open()
	put(first, 1, "# test log\ngetenv HOME\n")
	put(first, 2, "compiled package")
	_ = first.Close()

	second := open()
	for _, action := range []byte{1, 2} {
		if _, ok, err := second.Get(id(action)); !ok || err != nil {
			t.Fatalf("entry %d of the same job: %v, %v, want a hit", action, ok, err)
		}
	}
	put(second, 3, "another package")
	_ = second.Close()

	if saved, err := gocacheprog.SaveList(job, "test-list"); saved != 2 || err != nil {
		t.Fatalf("saved a list of %d entries, %v, want the 2 shared ones", saved, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	var list []byte
	for _, entry := range service.order {
		if isList(entry) {
			list = service.committed[entry]
		}
	}
	if !bytes.Equal(list, append(id(2), id(3)...)) {
		t.Errorf("the job's list is %x, want the two shared entries in the order the job first used them", list)
	}
}

// TestJobReplacesAListItCouldNotUse: a job that does not share test results
// may start from the list of a run that did. It rejects the test records in
// it, and saves a list without them, so its next run asks for none of them.
func TestJobReplacesAListItCouldNotUse(t *testing.T) {
	srv, service := newFakeCacheService()
	defer srv.Close()
	const listKey = "test-list"
	job := func(shareTestResults bool) gocacheprog.Options {
		return gocacheprog.Options{Dir: t.TempDir(), ResultsURL: srv.URL, Token: "test-token", Namespace: "test", ShareTestResults: shareTestResults}
	}
	id := func(action byte) []byte { return bytes.Repeat([]byte{action}, 32) }

	sharing := job(true)
	c, err := gocacheprog.New(sharing)
	if err != nil {
		t.Fatal(err)
	}
	for action, output := range map[byte]string{1: "# test log\ngetenv HOME\n", 2: "compiled package"} {
		outputID := sha256.Sum256([]byte(output))
		if _, err := c.Put(id(action), outputID[:], []byte(output)); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Close()
	if saved, err := gocacheprog.SaveList(sharing, listKey); saved != 2 || err != nil {
		t.Fatalf("the sharing job saved a list of %d entries, %v, want 2", saved, err)
	}

	// Two runs of the job without sharing: each prefetches, uses the build
	// output, and saves its list.
	run := func() (saved, downloads int) {
		t.Helper()
		opts := job(false)
		before := service.downloadCount()
		if err := gocacheprog.Prefetch(opts, listKey); err != nil {
			t.Fatal(err)
		}
		c, err := gocacheprog.New(opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := c.Get(id(2)); !ok || err != nil {
			t.Fatalf("the build output after the prefetch: %v, %v, want a hit", ok, err)
		}
		_ = c.Close()
		saved, err = gocacheprog.SaveList(opts, listKey)
		if err != nil {
			t.Fatal(err)
		}
		return saved, service.downloadCount() - before
	}
	if saved, _ := run(); saved != 1 {
		t.Errorf("a job that could not use its whole list saved %d entries, want the 1 it used", saved)
	}
	// The list and the build output, and no longer the test record.
	if saved, downloads := run(); saved != 0 || downloads != 2 {
		t.Errorf("the next run saved %d entries and made %d downloads, want 0 and 2", saved, downloads)
	}
}
