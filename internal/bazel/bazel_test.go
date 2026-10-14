package bazel

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

func TestConfigureWritesTheRegisteredCacheIntoTheSystemRC(t *testing.T) {
	const cachePath = "/bazel/0123456789abcdef0123456789abcdef"
	agent := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case status != http.StatusOK:
				w.WriteHeader(status)
			case r.Method != http.MethodPost || r.URL.Path != registerPath || r.Header.Get("Authorization") != "Bearer job-token":
				w.WriteHeader(http.StatusUnauthorized)
			default:
				_, _ = w.Write([]byte(`{"path":"` + cachePath + `"}`))
			}
		}))
	}
	block := func(server *httptest.Server) string {
		return blockStart + "\ncommon --remote_cache=" + server.URL + cachePath + "\n" + blockEnd + "\n"
	}

	for _, tc := range []struct {
		name       string
		mode       string
		noMagic    bool
		token      string
		status     int
		existing   string
		want       func(*httptest.Server) string
		wantWarned string
	}{
		{name: "no system rc yet", want: block},
		{
			name:     "keeps the image's settings",
			existing: "build --jobs=8\n\nbuild --verbose_failures\n",
			want:     func(s *httptest.Server) string { return "build --jobs=8\n\nbuild --verbose_failures\n" + block(s) },
		},
		{
			name:     "replaces the block of an earlier step",
			existing: "build --jobs=8\n" + blockStart + "\ncommon --remote_cache=http://10.0.0.9:6871/bazel/old\n" + blockEnd + "\n",
			want:     func(s *httptest.Server) string { return "build --jobs=8\n" + block(s) },
		},
		{name: "agent without the Bazel cache", status: http.StatusNotFound, existing: "build --jobs=8\n", wantWarned: "v3.5.1"},
		{name: "agent refuses the token", status: http.StatusUnauthorized, wantWarned: "401"},
		{name: "no Magic Cache", noMagic: true, wantWarned: "needs Magic Cache"},
		{name: "no runtime token", token: "-", wantWarned: "ACTIONS_RUNTIME_TOKEN"},
		{name: "unknown mode", mode: "read-only", wantWarned: `"read-only"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			server := agent(status)
			defer server.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", server.URL+"/")
			rc := filepath.Join(t.TempDir(), "bazel.bazelrc")
			if tc.existing != "" {
				if err := os.WriteFile(rc, []byte(tc.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			previous := systemRC
			systemRC = func() string { return rc }
			defer func() { systemRC = previous }()
			mode, token := tc.mode, tc.token
			if mode == "" {
				mode = "true"
			}
			switch token {
			case "":
				token = "job-token"
			case "-":
				token = ""
			}
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), mode, !tc.noMagic, token)

			got, _ := os.ReadFile(rc)
			want := tc.existing
			if tc.want != nil {
				want = tc.want(server)
			}
			if string(got) != want {
				t.Errorf("system rc:\n%s\nwant:\n%s", got, want)
			}
			warned := strings.Contains(log.String(), "::warning::")
			if warned != (tc.wantWarned != "") || !strings.Contains(log.String(), tc.wantWarned) {
				t.Errorf("log %q, want a warning containing %q", log.String(), tc.wantWarned)
			}
			if tc.want != nil && !strings.Contains(log.String(), "::add-mask::"+server.URL+cachePath) {
				t.Errorf("the cache URL is not masked in the job's logs: %q", log.String())
			}
			if strings.Contains(string(got), "job-token") || strings.Contains(log.String(), "job-token") {
				t.Error("the runtime token reached the system rc or the log")
			}
		})
	}
}
