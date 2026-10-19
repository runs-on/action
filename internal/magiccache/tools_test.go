package magiccache

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// The agent's registration contract: POST /tools with the runtime token
// answers the job's path; older agents answer 404 or 405; a POST without a
// token answers 401 on agents that serve the tool caches.
func TestRegister(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "registered", status: http.StatusOK, body: `{"path":"/tools/` + id + `"}`},
		{name: "older agent", status: http.StatusNotFound, wantErr: ErrToolsNotServed},
		{name: "older agent, other route", status: http.StatusMethodNotAllowed, wantErr: ErrToolsNotServed},
		{name: "refused token", status: http.StatusForbidden, wantErr: errAny},
		{name: "redirect", status: http.StatusTemporaryRedirect, wantErr: errAny},
		{name: "path outside /tools", status: http.StatusOK, body: `{"path":"/bazel/` + id + `"}`, wantErr: errAny},
		{name: "path that is not an ID", status: http.StatusOK, body: `{"path":"/tools/../x"}`, wantErr: errAny},
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
				if tools.ID != id || tools.URL != agent.URL+"/tools/"+id {
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

func TestServesTools(t *testing.T) {
	for status, want := range map[int]bool{http.StatusUnauthorized: true, http.StatusNotFound: false, http.StatusMethodNotAllowed: false} {
		agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("the probe sent a token")
			}
			w.WriteHeader(status)
		}))
		t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
		if got := ServesTools(); got != want {
			t.Errorf("ServesTools on an agent answering %d = %v, want %v", status, got, want)
		}
		agent.Close()
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
		if _, ok := RegisterFor(action, input, "no cache", true, "shared-token"); ok {
			t.Fatalf("%s registered with an agent that refuses", input)
		}
	}
	if posts != 1 {
		t.Errorf("the agent got %d registrations, want 1", posts)
	}
}
