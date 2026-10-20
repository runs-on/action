package magiccache

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// The agent's registration contract: POST /_runs-on/tools with the runtime
// token answers the job's ID and the path of each protocol it serves, which
// the action uses as given on the same agent; older agents answer 404 or 405.
func TestRegister(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	reply := func(paths string) string { return `{"id":"` + id + `","protocols":{` + paths + `}}` }
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantURLs map[string]string
		wantErr  error
	}{
		{
			name: "registered", status: http.StatusOK,
			body:     reply(`"bazel":"/_runs-on/tools/` + id + `/bazel","kv":"/_runs-on/kv/` + id + `"`),
			wantURLs: map[string]string{"bazel": "/_runs-on/tools/" + id + "/bazel", "kv": "/_runs-on/kv/" + id},
		},
		{name: "older agent", status: http.StatusNotFound, wantErr: ErrToolsNotServed},
		{name: "older agent, other route", status: http.StatusMethodNotAllowed, wantErr: ErrToolsNotServed},
		{name: "refused token", status: http.StatusForbidden, wantErr: errAny},
		{name: "redirect", status: http.StatusTemporaryRedirect, wantErr: errAny},
		{name: "ID that is not one", status: http.StatusOK, body: `{"id":"../x","protocols":{}}`, wantErr: errAny},
		{name: "path on another host", status: http.StatusOK, body: reply(`"kv":"//elsewhere.example/kv"`), wantErr: errAny},
		{name: "URL", status: http.StatusOK, body: reply(`"kv":"http://elsewhere.example/kv"`), wantErr: errAny},
		{name: "relative path", status: http.StatusOK, body: reply(`"kv":"kv"`), wantErr: errAny},
		{name: "dot segment", status: http.StatusOK, body: reply(`"kv":"/_runs-on/tools/../kv"`), wantErr: errAny},
		{name: "query", status: http.StatusOK, body: reply(`"kv":"/kv?x=1"`), wantErr: errAny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != ToolsPath || r.Header.Get("Authorization") != "Bearer job-token" {
					t.Errorf("unexpected registration %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Location", "/elsewhere")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer agent.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")

			tools, err := Register("job-token")
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("Register: %v", err)
			case tc.wantErr == nil:
				want := map[string]string{}
				for protocol, path := range tc.wantURLs {
					want[protocol] = agent.URL + path
				}
				if tools.ID != id || !maps.Equal(tools.URLs, want) {
					t.Errorf("Register = %+v, want URLs %v", tools, want)
				}
			case tc.wantErr == errAny && err == nil, tc.wantErr != errAny && !errors.Is(err, tc.wantErr):
				t.Errorf("Register error = %v, want %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "job-token") {
				t.Error("the error holds the runtime token")
			}
		})
	}
}

// errAny stands for any error in TestRegister.
var errAny = errors.New("any error")

// Serves reads the protocols the agent lists without a token; agents before
// remote caches answer 404.
func TestServes(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{status: http.StatusOK, body: `{"version":1,"protocols":["bazel","sccache","kv"]}`, want: true},
		{status: http.StatusOK, body: `{"version":1,"protocols":["kv"]}`},
		{status: http.StatusOK, body: `not json`},
		{status: http.StatusNotFound},
	} {
		agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/_runs-on/tools" || r.Header.Get("Authorization") != "" {
				t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
		if got := Serves("sccache"); got != tc.want {
			t.Errorf("Serves(sccache) on an agent answering %d %q = %v, want %v", tc.status, tc.body, got, tc.want)
		}
		agent.Close()
	}
}

// An agent that gives a tool's protocol no path gets a warning, and the tool
// is left alone; a tool whose protocol has a path gets its URL, and later
// steps the key/value cache's.
func TestRegisterForChecksTheProtocol(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + id + `","protocols":{"kv":"/_runs-on/tools/` + id + `/kv"}}`))
	}))
	defer agent.Close()
	t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("GITHUB_ENV", envFile)
	t.Setenv(RemoteCacheURLEnv, "")
	var log bytes.Buffer
	action := githubactions.New(githubactions.WithWriter(&log))
	if _, ok := RegisterFor(action, "bazel_remote_cache", "bazel", "no cache", true, "token-a"); ok {
		t.Error("registered for a protocol the agent does not serve")
	}
	if !strings.Contains(log.String(), `"bazel" tool cache`) {
		t.Errorf("no warning naming the protocol: %q", log.String())
	}
	if exported, _ := os.ReadFile(envFile); len(exported) > 0 {
		t.Errorf("a tool left alone exported %q", exported)
	}
	want := agent.URL + "/_runs-on/tools/" + id + "/kv"
	if got, ok := RegisterFor(action, "gocacheprog", "kv", "no cache", true, "token-a"); !ok || got != want {
		t.Errorf("RegisterFor(kv) = %q, %v, want %q: %q", got, ok, want, log.String())
	}
	if exported, _ := os.ReadFile(envFile); !strings.Contains(string(exported), want) || os.Getenv(RemoteCacheURLEnv) != want {
		t.Errorf("exported %q, want %s=%s for later steps and this step's children", exported, RemoteCacheURLEnv, want)
	}
}

// The tool inputs of one action step share its registration: an agent that
// stops answering costs one timeout, not one per tool.
func TestRegisterForRegistersOncePerAgentAndToken(t *testing.T) {
	var posts int
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer agent.Close()
	t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
	action := githubactions.New(githubactions.WithWriter(io.Discard))
	for _, input := range []string{"bazel_remote_cache", "sccache: true", "gocacheprog"} {
		if _, ok := RegisterFor(action, input, "kv", "no cache", true, "shared-token"); ok {
			t.Fatalf("%s registered with an agent that refuses", input)
		}
	}
	if posts != 1 {
		t.Errorf("the agent got %d registrations, want 1", posts)
	}
}
