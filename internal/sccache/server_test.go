package sccache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// fakeSccache makes the test binary act as sccache when it names a file: the
// environment a started server would get is written there, and the stats
// report the storage sccache would pick from it.
const fakeSccache = "RUNS_ON_TEST_FAKE_SCCACHE"

// The fake server refuses Magic Cache's writes, or does not start.
const (
	fakeRefusedWrites = "RUNS_ON_TEST_REFUSED_WRITES"
	fakeStartFails    = "RUNS_ON_TEST_START_FAILS"
)

func TestMain(m *testing.M) {
	if record := os.Getenv(fakeSccache); record != "" {
		if slices.Contains(os.Args[1:], "--start-server") {
			if os.Getenv(fakeStartFails) != "" {
				fmt.Fprintln(os.Stderr, "sccache: error: Server startup failed: cache storage failed to read")
				os.Exit(2)
			}
			if os.Getenv(fakeRefusedWrites) != "" {
				log, _ := os.OpenFile(os.Getenv("SCCACHE_ERROR_LOG"), os.O_APPEND|os.O_WRONLY, 0)
				fmt.Fprintln(log, "storage write check failed: Unexpected (permanent) at write")
			}
			_ = os.WriteFile(record, []byte(strings.Join(os.Environ(), "\n")), 0o644)
		}
		if slices.Contains(os.Args[1:], "--show-stats") {
			server, _ := os.ReadFile(record)
			env := strings.Split(string(server), "\n")
			// sccache prefers S3 to the GitHub Actions cache.
			location := "Local disk"
			if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "SCCACHE_BUCKET=") }) {
				location = "s3, name: bucket, prefix: /"
			} else if slices.Contains(env, "SCCACHE_GHA_ENABLED=true") {
				location = "ghac, name: version, prefix: /sccache/"
			}
			fmt.Printf(`{"cache_location":%q}`, location)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// installFakeSccache puts the test binary on PATH as sccache.
func installFakeSccache(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "sccache"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestMagicCacheStartsTheServerWithTheTokenAndExportsOnlyTheWrapper(t *testing.T) {
	for _, tc := range []struct {
		name        string
		magicCache  bool
		token       string
		installed   bool
		env         map[string]string
		wantServer  bool
		wantExport  bool
		wantWarning string
	}{
		{name: "Magic Cache", magicCache: true, token: "job-token", installed: true, wantServer: true, wantExport: true},
		{name: "a stack that refuses the writes", magicCache: true, token: "job-token", installed: true, env: map[string]string{fakeRefusedWrites: "1"}, wantServer: true, wantExport: true, wantWarning: "v3.5.0"},
		{name: "a server that does not start", magicCache: true, token: "job-token", installed: true, env: map[string]string{fakeStartFails: "1"}, wantWarning: "Server startup failed"},
		{name: "S3 settings left from an earlier step", magicCache: true, token: "job-token", installed: true, env: map[string]string{"SCCACHE_BUCKET": "bucket"}, wantServer: true, wantExport: true, wantWarning: "not in Magic Cache"},
		{name: "a relative error log", magicCache: true, token: "job-token", installed: true, env: map[string]string{"SCCACHE_ERROR_LOG": "sccache.log"}, wantServer: true, wantExport: true},
		{name: "sccache not installed", magicCache: true, token: "job-token", wantWarning: "install it before this action"},
		{name: "no Magic Cache", token: "job-token", installed: true, wantWarning: "needs Magic Cache"},
		{name: "no runtime token", magicCache: true, installed: true, wantWarning: "ACTIONS_RUNTIME_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.installed {
				installFakeSccache(t)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			record := filepath.Join(t.TempDir(), "server-env")
			t.Setenv(fakeSccache, record)
			t.Chdir(t.TempDir())
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			t.Setenv("RUNNER_TEMP", t.TempDir())
			t.Setenv("SCCACHE_ERROR_LOG", "")
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", "http://10.0.0.1:6871/")
			t.Setenv("ACTIONS_CACHE_URL", "https://artifactcache.example.com/")
			t.Setenv("INPUT_TOKEN", "github-token")
			t.Setenv("SCCACHE_GHA_VERSION", "job-namespace")
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), "true", tc.magicCache, tc.token)

			server, _ := os.ReadFile(record)
			serverEnv := strings.Split(string(server), "\n")
			if started := len(server) > 0; started != tc.wantServer {
				t.Fatalf("server started: %v, want %v; log:\n%s", started, tc.wantServer, log.String())
			}
			if tc.wantServer {
				for _, want := range []string{
					"ACTIONS_RUNTIME_TOKEN=job-token",
					"ACTIONS_RESULTS_URL=http://10.0.0.1:6871/",
					"ACTIONS_CACHE_SERVICE_V2=true",
					"SCCACHE_GHA_ENABLED=true",
					"SCCACHE_IDLE_TIMEOUT=0",
					"SCCACHE_GHA_VERSION=job-namespace",
				} {
					if !slices.Contains(serverEnv, want) {
						t.Errorf("the server's environment lacks %s", want)
					}
				}
				// The server opens its log from sccache's own directory.
				for _, kv := range serverEnv {
					if name, value, _ := strings.Cut(kv, "="); name == "SCCACHE_ERROR_LOG" && !filepath.IsAbs(value) {
						t.Errorf("the server's error log is %q, want an absolute path", value)
					}
				}
				for _, unwanted := range []string{"INPUT_TOKEN=", "ACTIONS_CACHE_URL="} {
					if slices.ContainsFunc(serverEnv, func(kv string) bool { return strings.HasPrefix(kv, unwanted) }) {
						t.Errorf("the server's environment has %s", unwanted)
					}
				}
			}
			exported, _ := os.ReadFile(envFile)
			if got := strings.Contains(string(exported), "RUSTC_WRAPPER"); got != tc.wantExport {
				t.Errorf("RUSTC_WRAPPER exported: %v, want %v; exported %q", got, tc.wantExport, exported)
			}
			if strings.Contains(string(exported), "job-token") || strings.Contains(log.String(), "job-token") {
				t.Errorf("the runtime token reached the log or the next steps' environment")
			}
			warned := strings.Contains(log.String(), "::warning")
			if warned != (tc.wantWarning != "") || !strings.Contains(log.String(), tc.wantWarning) {
				t.Errorf("log %q, want a warning about %q", log.String(), tc.wantWarning)
			}
		})
	}
}
