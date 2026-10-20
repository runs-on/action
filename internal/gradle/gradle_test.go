package gradle

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigurePointsGradleAtTheAgentsCache(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name       string
		mode       string
		noMagic    bool
		token      string
		status     int
		wantWarned string
	}{
		{name: "registered", mode: "true", token: "job-token"},
		{name: "agent without the tool caches", mode: "true", token: "job-token", status: http.StatusNotFound, wantWarned: "v3.6.0"},
		{name: "no Magic Cache", mode: "true", token: "job-token", noMagic: true, wantWarned: "needs Magic Cache"},
		{name: "no runtime token", mode: "true", wantWarned: "ACTIONS_RUNTIME_TOKEN"},
		{name: "unknown mode", mode: "push", token: "job-token", wantWarned: `"push"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/_runs-on/tools" || r.Header.Get("Authorization") != "Bearer job-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(`{"path":"/_runs-on/tools/` + id + `","protocols":["bazel","sccache","kv"]}`))
			}))
			defer agent.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			t.Setenv("GITHUB_ENV", filepath.Join(t.TempDir(), "env"))
			t.Setenv("GRADLE_USER_HOME", t.TempDir())
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, !tc.noMagic, tc.token)

			script, err := os.ReadFile(initScriptPath())
			warned := strings.Contains(log.String(), "::warning::")
			if tc.wantWarned != "" {
				if !warned || !strings.Contains(log.String(), tc.wantWarned) {
					t.Errorf("log %q, want a warning containing %q", log.String(), tc.wantWarned)
				}
				if err == nil {
					t.Error("the init script was written")
				}
				return
			}
			if warned || err != nil {
				t.Fatalf("no init script (%v); log %q", err, log.String())
			}
			if want := "url = '" + agent.URL + "/_runs-on/tools/" + id + "/kv/gradle/'"; !strings.Contains(string(script), want) {
				t.Errorf("init script lacks %q:\n%s", want, script)
			}
			if !strings.Contains(log.String(), "::add-mask::"+id) {
				t.Error("the cache path is not masked in the job's logs")
			}
			if strings.Contains(string(script)+log.String(), "job-token") {
				t.Error("the runtime token reached the init script or the log")
			}
			if info, _ := os.Stat(initScriptPath()); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Errorf("the init script has mode %v, want 0600", info.Mode().Perm())
			}

			Finish()
			if _, err := os.Stat(initScriptPath()); !os.IsNotExist(err) {
				t.Errorf("the post step left the init script: %v", err)
			}
		})
	}
}

// The post step removes the init script it wrote, and nothing else at its
// path.
func TestFinishLeavesOtherScripts(t *testing.T) {
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	path := initScriptPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("// the user's own script\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Finish()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Finish removed a script it did not write: %v", err)
	}
}
