package gocacheprog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureExportsGOCACHEPROGOnlyWithMagicCacheAndAToken(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/tools" || r.Header.Get("Authorization") != "Bearer job-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"path":"/tools/` + id + `"}`))
	}))
	defer agent.Close()
	t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
	started := startPrefetch
	defer func() { startPrefetch = started }()
	for _, tc := range []struct {
		name       string
		mode       string
		magicCache bool
		token      string
		want       []string
		wantShared bool
		// wantPrefetch is whether each prefetch started shares test results.
		wantPrefetch []bool
	}{
		{name: "builds only", mode: "true", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag}, wantPrefetch: []bool{false}},
		{name: "with test results", mode: "with-tests", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag}, wantShared: true, wantPrefetch: []bool{true}},
		{name: "unknown mode", mode: "tests", magicCache: true, token: "job-token"},
		{name: "no Magic Cache", mode: "true", token: "job-token"},
		{name: "no runtime token", mode: "true", magicCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			t.Setenv("RUNNER_TEMP", t.TempDir())
			t.Setenv("ACTIONS_RUNTIME_TOKEN", "")
			var log bytes.Buffer
			var prefetches []bool
			startPrefetch = func(_ string, shareTestResults bool) error {
				prefetches = append(prefetches, shareTestResults)
				return nil
			}

			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, tc.magicCache, tc.token)

			if !slices.Equal(prefetches, tc.wantPrefetch) {
				t.Errorf("prefetches started: %v, want %v", prefetches, tc.wantPrefetch)
			}

			exported, _ := os.ReadFile(envFile)
			for _, want := range tc.want {
				if !strings.Contains(string(exported), want) {
					t.Errorf("exported env %q lacks %q", exported, want)
				}
			}
			if shared := strings.Contains(string(exported), ShareTestResultsFlag); shared != tc.wantShared {
				t.Errorf("exported env shares test results: %v, want %v", shared, tc.wantShared)
			}
			if len(tc.want) == 0 && len(exported) > 0 {
				t.Errorf("exported %q, want nothing", exported)
			}
			if strings.Contains(log.String(), "job-token") || strings.Contains(string(exported), "job-token") {
				t.Errorf("the runtime token reached the log or the next steps' environment")
			}
			// The helper, started from a run step, reads the endpoint Configure
			// kept for it, in a file only the runner's user reads: its path
			// stands for the job's token.
			wantStored := ""
			if len(tc.want) > 0 {
				wantStored = agent.URL + "/tools/" + id + "/gocacheprog"
				if !strings.Contains(log.String(), "::add-mask::"+id) {
					t.Errorf("the path ID is not masked in the job's logs")
				}
			}
			if got := OptionsFromEnv(false).Endpoint; got != wantStored {
				t.Errorf("the helper's endpoint is %q, want %q: the job's only when GOCACHEPROG is exported", got, wantStored)
			}
			if strings.Contains(string(exported), id) {
				t.Errorf("the path ID reached the next steps' environment")
			}
			if info, err := os.Stat(filepath.Join(os.Getenv("RUNNER_TEMP"), endpointFile)); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Errorf("the endpoint file has mode %v, want 0600", info.Mode().Perm())
			}
		})
	}
}

// runtimeToken is a token as GitHub signs them, reduced to the claim that
// names the job: "<run>.<job>.<matrix leg>".
func runtimeToken(orchID string) string {
	payload, _ := json.Marshal(map[string]string{"orch_id": orchID})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestListKeyIsTheJobAcrossRuns(t *testing.T) {
	t.Setenv("GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/ci.yml@refs/heads/main")
	t.Setenv("GITHUB_JOB", "build")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	leg := ListKey(runtimeToken("run-1.build.ubuntu-latest_1_26"))

	for _, tc := range []struct {
		name  string
		token string
		env   [2]string
		same  bool
	}{
		{name: "the same job in another run", token: runtimeToken("run-2.build.ubuntu-latest_1_26"), same: true},
		{name: "the same job on another branch", token: runtimeToken("run-2.build.ubuntu-latest_1_26"), env: [2]string{"GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/ci.yml@refs/pull/7/merge"}, same: true},
		{name: "another leg of the matrix", token: runtimeToken("run-1.build.ubuntu-latest_1_25")},
		{name: "another job", token: runtimeToken("run-1.test.ubuntu-latest_1_26")},
		{name: "the same job name in another workflow", token: runtimeToken("run-1.build.ubuntu-latest_1_26"), env: [2]string{"GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/release.yml@refs/heads/main"}},
		{name: "another architecture", token: runtimeToken("run-1.build.ubuntu-latest_1_26"), env: [2]string{"RUNNER_ARCH", "ARM64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env[0] != "" {
				t.Setenv(tc.env[0], tc.env[1])
			}
			if got := ListKey(tc.token); (got == leg) != tc.same {
				t.Errorf("list key %q, same as the first job's %q: %v, want %v", got, leg, got == leg, tc.same)
			}
		})
	}

	// A token that does not name the job falls back to the job's key in its
	// workflow, where the legs of a matrix are one job.
	if a, b := ListKey("not-a-jwt"), ListKey(runtimeToken("")); a != b || a == leg {
		t.Errorf("list keys without a job claim: %q and %q, want the same one, apart from %q", a, b, leg)
	}
}

// A cache that accepts the list and then stops answering must not hold up
// the action's post step.
func TestSaveListGivesUpOnACacheThatStopsAnswering(t *testing.T) {
	var srv *httptest.Server
	stopped := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stopped
	}))
	defer srv.Close()
	defer close(stopped)
	timeout := listUploadTimeout
	listUploadTimeout = 200 * time.Millisecond
	defer func() { listUploadTimeout = timeout }()

	opts := Options{Dir: t.TempDir(), Endpoint: srv.URL + "/tools/test/gocacheprog", Namespace: "test"}
	if err := os.WriteFile(filepath.Join(opts.Dir, usedFile), bytes.Repeat([]byte{1}, actionIDSize), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if saved, err := SaveList(opts, "test-list"); saved != 0 || err == nil {
		t.Errorf("saved %d entries, %v, want an error", saved, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the post step waited %v for a cache that stopped answering", elapsed.Round(time.Second))
	}
}
