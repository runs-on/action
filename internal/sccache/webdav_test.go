package sccache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// registration is an agent's registration reply, with each protocol at the
// path a v3.6.0 agent gives it.
func registration(id string, protocols ...string) string {
	paths := map[string]string{}
	for _, protocol := range protocols {
		paths[protocol] = "/_runs-on/tools/" + id + "/" + protocol
	}
	body, _ := json.Marshal(map[string]any{"id": id, "protocols": paths})
	return string(body)
}

func TestWebDAVRegistration(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		success bool
	}{
		{"available", 200, registration(id, "bazel", "sccache", "kv"), true},
		{"agent without sccache's cache", 200, registration(id, "kv"), false},
		{"old agent", 404, "", false},
		{"broker refusal", 403, "do-not-log-response-body", false},
		{"redirect", 307, "", false},
		{"malformed", 200, `{"id":"bad\nid","protocols":{"sccache":"/sccache"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/_runs-on/tools" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
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
			ConfigureRemoteCache(githubactions.New(githubactions.WithWriter(&log)), true, "runtime-secret")
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
			want := map[string]string{"SCCACHE_GHA_ENABLED": "false", "SCCACHE_WEBDAV_ENDPOINT": server.URL + "/_runs-on/tools/" + id + "/sccache", "RUSTC_WRAPPER": "sccache", "RUNS_ON_REMOTE_CACHE_URL": server.URL + "/_runs-on/tools/" + id + "/kv"}
			if len(exports) != len(want) {
				t.Fatalf("unexpected exports: %d", len(exports))
			}
			for key, value := range want {
				if exports[key] != value {
					t.Errorf("incorrect %s", key)
				}
			}
			if !strings.Contains(log.String(), "::add-mask::"+id) {
				t.Error("the path ID is not masked")
			}
		})
	}
}

// sccache ranks WebDAV below most of its storages, so a setting left for one
// of them would silently win over the agent's cache.
func TestWebDAVWarnsAboutSettingsSccachePrefers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, registration(strings.Repeat("b", 32), "bazel", "sccache", "kv"))
	}))
	defer server.Close()
	competing := []string{"SCCACHE_BUCKET", "SCCACHE_REDIS", "SCCACHE_REDIS_ENDPOINT", "SCCACHE_REDIS_CLUSTER_ENDPOINTS", "SCCACHE_MEMCACHED", "SCCACHE_MEMCACHED_ENDPOINT", "SCCACHE_GCS_BUCKET", "SCCACHE_GHA_VERSION", "SCCACHE_AZURE_BLOB_CONTAINER", "SCCACHE_GHA_ENABLED", "SCCACHE_MULTILEVEL_CHAIN", "SCCACHE_WEBDAV_TOKEN", "SCCACHE_WEBDAV_USERNAME", "SCCACHE_WEBDAV_PASSWORD"}
	for _, tc := range []struct {
		name, value string
		warns       bool
		// leftAlone is for credentials sccache would send to the agent.
		leftAlone bool
	}{
		{name: "SCCACHE_BUCKET", value: "left-from-s3", warns: true},
		{name: "SCCACHE_REDIS_ENDPOINT", value: "redis://cache", warns: true},
		{name: "SCCACHE_GHA_VERSION", value: "namespace", warns: true},
		{name: "SCCACHE_GHA_ENABLED", value: "on", warns: true},
		{name: "SCCACHE_GHA_ENABLED", value: "false"},
		{name: "SCCACHE_BUCKET", value: ""},
		{name: "SCCACHE_WEBDAV_KEY_PREFIX", value: "namespace"},
		{name: "SCCACHE_MULTILEVEL_CHAIN", value: "disk,s3", warns: true},
		{name: "SCCACHE_MULTILEVEL_CHAIN", value: "disk, WebDAV"},
		{name: "SCCACHE_WEBDAV_TOKEN", value: "another-servers-token", warns: true, leftAlone: true},
		{name: "SCCACHE_WEBDAV_PASSWORD", value: "another-servers-password", warns: true, leftAlone: true},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			for _, name := range competing {
				t.Setenv(name, "")
			}
			t.Setenv(tc.name, tc.value)
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", server.URL+"/")
			t.Setenv("GITHUB_ENV", filepath.Join(t.TempDir(), "env"))
			var log bytes.Buffer

			ConfigureRemoteCache(githubactions.New(githubactions.WithWriter(&log)), true, "runtime-secret")

			warned := strings.Contains(log.String(), "::warning::These settings") && strings.Contains(log.String(), tc.name)
			if warned != tc.warns {
				t.Errorf("warned about %s: %v, want %v; log:\n%s", tc.name, warned, tc.warns, log.String())
			}
			exported, _ := os.ReadFile(os.Getenv("GITHUB_ENV"))
			if configured := strings.Contains(string(exported), "SCCACHE_WEBDAV_ENDPOINT"); configured == tc.leftAlone {
				t.Errorf("sccache: true configured WebDAV: %v, want %v (%q)", configured, !tc.leftAlone, exported)
			}
		})
	}
}
