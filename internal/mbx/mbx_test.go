package mbx

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

func TestConfigurePointsMbxAtTheAgentsCache(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	urls := map[string]bool{}
	for _, tc := range []struct {
		name       string
		noMagic    bool
		token      string
		status     int
		env        map[string]string
		wantNS     string
		wantWarned string
	}{
		{name: "registered", token: "job-token", wantNS: "1234"},
		{name: "the job's own namespace", token: "job-token", env: map[string]string{"MBX_REMOTE_NAMESPACE": "team/app"}},
		{name: "agent without remote caches", token: "job-token", status: http.StatusNotFound, wantWarned: "v3.6.0"},
		{name: "no Magic Cache", token: "job-token", noMagic: true, wantWarned: "needs Magic Cache"},
		{name: "no runtime token", wantWarned: "ACTIONS_RUNTIME_TOKEN"},
		{name: "mbx credentials", token: "job-token", env: map[string]string{"MBX_REMOTE_TOKEN": "secret"}, wantWarned: "plain HTTP"},
		{name: "S3 settings", token: "job-token", env: map[string]string{"MBX_REMOTE_S3_REGION": "us-west-2"}, wantWarned: "MBX_REMOTE_S3_REGION"},
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
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			t.Setenv("GITHUB_ENV", envFile)
			t.Setenv("GITHUB_REPOSITORY_ID", "1234")
			for _, name := range []string{"MBX_REMOTE_NAMESPACE", "MBX_REMOTE_TOKEN", "MBX_REMOTE_TOKEN_FILE", "MBX_REMOTE_OIDC_AUDIENCE", "MBX_REMOTE_S3_REGION"} {
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
				if strings.Contains(string(exported), "MBX_REMOTE_URL") {
					t.Error("MBX_REMOTE_URL was exported")
				}
				return
			}
			if warned {
				t.Fatalf("unexpected warning: %q", log.String())
			}
			// Each namespace has its own path below the mbx namespace.
			base := agent.URL + "/_runs-on/tools/" + id + "/kv/mbx/"
			url := exportedValue(string(exported), "MBX_REMOTE_URL")
			if !strings.HasPrefix(url, base) || !strings.HasSuffix(url, "/") || strings.Count(url[len(base):], "/") != 1 {
				t.Errorf("MBX_REMOTE_URL is %q, want a path below %s", url, base)
			}
			// Each case has its own fake agent: compare the paths below it.
			path := strings.TrimPrefix(url, base)
			if urls[path] {
				t.Errorf("MBX_REMOTE_URL %q has another namespace's path", url)
			}
			urls[path] = true
			if got := strings.Contains(string(exported), "MBX_REMOTE_NAMESPACE"); got != (tc.wantNS != "") || !strings.Contains(string(exported), tc.wantNS) {
				t.Errorf("exported %q, want MBX_REMOTE_NAMESPACE %q only when the job sets none", exported, tc.wantNS)
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
