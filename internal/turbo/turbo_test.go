package turbo

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

func TestConfigurePointsTurboAtTheAgentsCache(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name       string
		noMagic    bool
		token      string
		writable   string // the registration's writable field, if any
		noTurbo    bool   // an agent without the turbo protocol
		env        map[string]string
		wantCache  string // the exported TURBO_CACHE
		wantWarned string
	}{
		{name: "registered", token: "job-token", writable: `,"writable":true`},
		{name: "fork pull request", token: "job-token", writable: `,"writable":false`, wantCache: readOnlyCache},
		{name: "fork pull request with the job's own TURBO_CACHE", token: "job-token", writable: `,"writable":false`, env: map[string]string{"TURBO_CACHE": "local:rw"}},
		{name: "the job's own remote cache", token: "job-token", env: map[string]string{"TURBO_TOKEN": "vercel-token", "TURBO_TEAM": "acme"}, wantWarned: "TURBO_TOKEN, TURBO_TEAM"},
		{name: "agent without the turbo protocol", token: "job-token", noTurbo: true, wantWarned: `"turbo" tool cache`},
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
				if !tc.noTurbo {
					protocols += `,"turbo":"/_runs-on/tools/` + id + `/turbo"`
				}
				_, _ = w.Write([]byte(`{"id":"` + id + `","protocols":{` + protocols + `}` + tc.writable + `}`))
			}))
			defer agent.Close()
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			t.Setenv("GITHUB_ENV", envFile)
			for _, name := range append(settings, "TURBO_CACHE") {
				t.Setenv(name, tc.env[name])
			}
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), !tc.noMagic, tc.token)

			exported, _ := os.ReadFile(envFile)
			warned := strings.Contains(log.String(), "::warning::")
			if tc.wantWarned != "" {
				if !warned || !strings.Contains(log.String(), tc.wantWarned) {
					t.Errorf("log %q, want a warning containing %q", log.String(), tc.wantWarned)
				}
				if strings.Contains(string(exported), "TURBO_") {
					t.Errorf("exported %q, want nothing for turbo", exported)
				}
				return
			}
			if warned {
				t.Fatalf("unexpected warning: %q", log.String())
			}
			want := map[string]string{
				"TURBO_API":   agent.URL + "/_runs-on/tools/" + id + "/turbo",
				"TURBO_TOKEN": placeholder,
				"TURBO_TEAM":  placeholder,
				"TURBO_CACHE": tc.wantCache,
			}
			for name, value := range want {
				if got := exportedValue(string(exported), name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			if strings.Contains(string(exported)+log.String(), "job-token") {
				t.Error("the runtime token reached the next steps or the log")
			}
			if !strings.Contains(log.String(), "::add-mask::"+id) {
				t.Error("the cache path is not masked in the job's logs")
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
