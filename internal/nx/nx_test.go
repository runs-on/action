package nx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigurePointsNxAtTheAgentsCache(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name       string
		noMagic    bool
		token      string
		noNx       bool // an agent without the nx protocol
		env        map[string]string
		wantWarned string
		exported   bool // exported despite the warning
	}{
		{name: "registered", token: "job-token"},
		{name: "the job's own cache server", token: "job-token", env: map[string]string{serverEnv: "https://cache.example.com"}, wantWarned: serverEnv},
		{name: "Nx Cloud", token: "job-token", env: map[string]string{"NX_CLOUD_ACCESS_TOKEN": "cloud"}, wantWarned: "NX_NO_CLOUD=true", exported: true},
		{name: "Nx Cloud turned off", token: "job-token", env: map[string]string{"NX_CLOUD_ACCESS_TOKEN": "cloud", "NX_NO_CLOUD": "true"}},
		{name: "agent without the nx protocol", token: "job-token", noNx: true, wantWarned: `"nx" tool cache`},
		{name: "no Magic Cache", token: "job-token", noMagic: true, wantWarned: "needs Magic Cache"},
		{name: "no runtime token", wantWarned: "ACTIONS_RUNTIME_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/_runs-on/tools" || r.Header.Get("Authorization") != "Bearer job-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				protocols := `"kv":"/_runs-on/tools/` + id + `/kv"`
				if !tc.noNx {
					protocols += `,"nx":"/_runs-on/tools/` + id + `/nx"`
				}
				_, _ = w.Write([]byte(`{"id":"` + id + `","protocols":{` + protocols + `},"writable":true}`))
			}))
			defer agent.Close()
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			t.Setenv("GITHUB_ENV", envFile)
			for _, name := range []string{serverEnv, "NX_CLOUD_ACCESS_TOKEN", "NX_NO_CLOUD"} {
				t.Setenv(name, tc.env[name])
			}
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), !tc.noMagic, tc.token)

			exported, _ := os.ReadFile(envFile)
			warned := strings.Contains(log.String(), "::warning::")
			if tc.wantWarned != "" && (!warned || !strings.Contains(log.String(), tc.wantWarned)) {
				t.Errorf("log %q, want a warning containing %q", log.String(), tc.wantWarned)
			}
			if tc.wantWarned == "" && warned {
				t.Fatalf("unexpected warning: %q", log.String())
			}
			want := ""
			if tc.wantWarned == "" || tc.exported {
				want = agent.URL + "/_runs-on/tools/" + id + "/nx"
			}
			if got := exportedValue(string(exported), serverEnv); got != want {
				t.Errorf("%s = %q, want %q", serverEnv, got, want)
			}
			if strings.Contains(string(exported)+log.String(), "job-token") {
				t.Error("the runtime token reached the next steps or the log")
			}
		})
	}
}

// exportedValue reads a variable from a GITHUB_ENV file.
func exportedValue(env, name string) string {
	lines := strings.Split(env, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, name+"<<") && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}
