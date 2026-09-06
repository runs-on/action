package cache

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

func TestUpdateZctionsConfigNormalizesLegacyV2Flag(t *testing.T) {
	for _, tt := range []struct {
		name    string
		flag    string
		session bool
		want    bool
	}{
		{name: "legacy enabled", flag: "on", session: true, want: true},
		{name: "legacy case and whitespace", flag: " ON ", session: true, want: true},
		{name: "canonical enabled", flag: "true", session: true},
		{name: "explicitly disabled", flag: "false", session: true},
		{name: "legacy disabled", flag: "off", session: true},
		{name: "absent flag", session: true},
		{name: "unknown flag", flag: "invalid", session: true},
		{name: "no RunsOn cache session", flag: "on"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const token = "test-runtime-token-do-not-log"
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			t.Setenv("ACTIONS_CACHE_SERVICE_V2", tt.flag)
			var log bytes.Buffer
			action := githubactions.New(githubactions.WithWriter(&log))
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPut || r.URL.Path != "/config" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("ACTIONS_RUNTIME_TOKEN") != token {
					t.Error("runtime token was not forwarded to the local cache service")
				}
				if r.Form.Get("ACTIONS_RESULTS_URL") != "https://results.example/" || r.Form.Get("ACTIONS_CACHE_URL") != "https://cache.example/" {
					t.Error("original backend URLs were not forwarded")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			resultsURL := ""
			if tt.session {
				resultsURL = "https://results.example/"
			}
			UpdateZctionsConfig(action, server.URL+"/", resultsURL, "https://cache.example/", token)
			data, err := os.ReadFile(envFile)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tt.want {
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) != 3 || !strings.HasPrefix(lines[0], "ACTIONS_CACHE_SERVICE_V2<<") || lines[1] != "true" || strings.TrimPrefix(lines[0], "ACTIONS_CACHE_SERVICE_V2<<") != lines[2] {
					t.Fatalf("invalid environment export: %q", data)
				}
			} else if len(data) != 0 {
				t.Fatalf("unexpected environment override: %q", data)
			}
			wantRequests := 0
			if tt.session {
				wantRequests = 1
			}
			if requests != wantRequests {
				t.Errorf("requests = %d, want %d", requests, wantRequests)
			}
			if strings.Contains(log.String(), token) {
				t.Error("runtime token leaked to action logs")
			}
		})
	}
}
