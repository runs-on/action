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
		noMagic    bool
		token      string
		status     int
		gradleHome bool
		wantWarned string
	}{
		{name: "registered", token: "job-token"},
		{name: "the job's own Gradle user home", token: "job-token", gradleHome: true},
		{name: "agent without remote caches", token: "job-token", status: http.StatusNotFound, wantWarned: "v3.6.0"},
		{name: "no Magic Cache", token: "job-token", noMagic: true, wantWarned: "needs Magic Cache"},
		{name: "no runtime token", wantWarned: "ACTIONS_RUNTIME_TOKEN"},
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
				_, _ = w.Write([]byte(`{"id":"` + id + `","protocols":{"bazel":"/_runs-on/tools/` + id + `/bazel","sccache":"/_runs-on/tools/` + id + `/sccache","kv":"/_runs-on/tools/` + id + `/kv"}}`))
			}))
			defer agent.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			// Without GRADLE_USER_HOME, the action exports the one it wrote the
			// script in, ~/.gradle, which Gradle would otherwise take from the
			// user database rather than HOME.
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			gradleHome := ""
			if tc.gradleHome {
				gradleHome = t.TempDir()
			}
			t.Setenv("GRADLE_USER_HOME", gradleHome)
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), !tc.noMagic, tc.token)
			exported, _ := os.ReadFile(envFile)
			if got, want := strings.Contains(string(exported), "GRADLE_USER_HOME"), tc.wantWarned == "" && !tc.gradleHome; got != want {
				t.Errorf("exported GRADLE_USER_HOME: %v, want %v (%q)", got, want, exported)
			}
			if tc.wantWarned == "" && !tc.gradleHome && !strings.Contains(string(exported), filepath.Join(home, ".gradle")) {
				t.Errorf("exported %q, want GRADLE_USER_HOME=%s", exported, filepath.Join(home, ".gradle"))
			}

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
