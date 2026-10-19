package sccache

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestWebDAVRegistration(t *testing.T) {
	credential := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		success bool
	}{
		{"available", 200, fmt.Sprintf(`{"token":%q}`, credential), true},
		{"old agent", 404, "", false},
		{"broker refusal", 403, "do-not-log-response-body", false},
		{"redirect", 307, "", false},
		{"malformed", 200, `{"token":"bad\ncredential"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/sccache" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
					t.Error("invalid registration request")
				}
				w.Header().Set("Location", "/unexpected-redirect")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", server.URL+"/")
			t.Setenv("ACTIONS_RESULTS_URL", "http://wrong-cache.invalid/")
			t.Setenv("PATH", t.TempDir()) // Configuration does not require a sccache binary.
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			var log bytes.Buffer
			Configure(githubactions.New(githubactions.WithWriter(&log)), "true", true, "runtime-secret")
			env, _ := os.ReadFile(envFile)
			if strings.Contains(string(env)+log.String(), "runtime-secret") || strings.Contains(log.String(), "do-not-log-response-body") {
				t.Fatal("registration leaked a secret")
			}
			if !tc.success {
				if len(env) != 0 {
					t.Fatal("failed registration exported configuration")
				}
				return
			}
			exports := map[string]string{}
			lines := strings.Split(strings.TrimSpace(string(env)), "\n")
			for i := 0; i < len(lines); i += 3 {
				key, _, _ := strings.Cut(lines[i], "<<")
				exports[key] = lines[i+1]
			}
			want := map[string]string{"SCCACHE_GHA_ENABLED": "false", "SCCACHE_WEBDAV_ENDPOINT": server.URL + "/sccache", "SCCACHE_WEBDAV_TOKEN": credential, "RUSTC_WRAPPER": "sccache"}
			if len(exports) != len(want) {
				t.Fatalf("unexpected exports: %d", len(exports))
			}
			for key, value := range want {
				if exports[key] != value {
					t.Errorf("incorrect %s", key)
				}
			}
			if !strings.Contains(log.String(), "::add-mask::"+credential) {
				t.Error("credential not masked")
			}
		})
	}
}
