package magiccache

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// The agent's registration contract: POST /_runs-on/tools with the runtime
// token answers the job's path and the protocols it serves; older agents
// answer 404 or 405.
func TestRegister(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "registered", status: http.StatusOK, body: `{"path":"/_runs-on/tools/` + id + `","protocols":["bazel","kv"]}`},
		{name: "older agent", status: http.StatusNotFound, wantErr: ErrToolsNotServed},
		{name: "older agent, other route", status: http.StatusMethodNotAllowed, wantErr: ErrToolsNotServed},
		{name: "refused token", status: http.StatusForbidden, wantErr: errAny},
		{name: "redirect", status: http.StatusTemporaryRedirect, wantErr: errAny},
		{name: "path outside the tool caches", status: http.StatusOK, body: `{"path":"/tools/` + id + `"}`, wantErr: errAny},
		{name: "path that is not an ID", status: http.StatusOK, body: `{"path":"/_runs-on/tools/../x"}`, wantErr: errAny},
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
				if tools.ID != id || tools.URL != agent.URL+"/_runs-on/tools/"+id || !slices.Equal(tools.Protocols, []string{"bazel", "kv"}) {
					t.Errorf("Register = %+v", tools)
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
// the tool caches answer 404.
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

// An agent that lacks a tool's protocol gets a warning, and the tool is left
// alone; a tool whose protocol it lists is configured.
func TestRegisterForChecksTheProtocol(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"path":"/_runs-on/tools/0123456789abcdef0123456789abcdef","protocols":["kv"]}`))
	}))
	defer agent.Close()
	t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
	t.Setenv("GITHUB_ENV", filepath.Join(t.TempDir(), "env"))
	var log bytes.Buffer
	action := githubactions.New(githubactions.WithWriter(&log))
	if _, ok := RegisterFor(action, "bazel_remote_cache", "bazel", "no cache", true, "token-a"); ok {
		t.Error("registered for a protocol the agent does not list")
	}
	if !strings.Contains(log.String(), `"bazel" tool cache`) {
		t.Errorf("no warning naming the protocol: %q", log.String())
	}
	if _, ok := RegisterFor(action, "gocacheprog", "kv", "no cache", true, "token-a"); !ok {
		t.Errorf("did not register for a protocol the agent lists: %q", log.String())
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
