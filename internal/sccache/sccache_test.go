package sccache

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureSccacheExportsResolvedPrefix(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "42")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	t.Setenv("RUNS_ON_S3_BUCKET_CACHE", "test-cache-bucket")
	t.Setenv("RUNS_ON_AWS_REGION", "us-east-1")
	t.Setenv("SCCACHE_S3_KEY_PREFIX", "cache/previous-invocation")
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("GITHUB_ENV", envFile)
	var log bytes.Buffer
	Configure(githubactions.New(githubactions.WithWriter(&log)), "s3", false, "")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	exports := make(map[string]string)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := 0; i < len(lines); i += 3 {
		key, delimiter, ok := strings.Cut(lines[i], "<<")
		if !ok || i+2 >= len(lines) || lines[i+2] != delimiter {
			t.Fatalf("invalid environment export: %q", data)
		}
		exports[key] = lines[i+1]
	}
	for key, want := range map[string]string{
		"SCCACHE_GHA_ENABLED": "false", "SCCACHE_BUCKET": "test-cache-bucket",
		"SCCACHE_REGION": "us-east-1", "SCCACHE_S3_KEY_PREFIX": "cache/sccache/42/linux-x64/v1", "RUSTC_WRAPPER": "sccache",
	} {
		if got := exports[key]; got != want {
			t.Errorf("exported %s = %q, want %q", key, got, want)
		}
	}
}

func TestDefaultKeyPrefixScopesRepositoryAndPlatform(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "123456789")
	t.Setenv("GITHUB_REPOSITORY", "runs-on/action")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")

	if got, want := DefaultKeyPrefix(), "cache/sccache/123456789/linux-x64/v1"; got != want {
		t.Fatalf("DefaultKeyPrefix() = %q, want %q", got, want)
	}
}

func TestDefaultKeyPrefixFallsBackToRepositorySlug(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "")
	t.Setenv("GITHUB_REPOSITORY", "Runs-On/Action")
	t.Setenv("RUNNER_OS", "Windows")
	t.Setenv("RUNNER_ARCH", "X64")

	if got, want := DefaultKeyPrefix(), "cache/sccache/runs-on/action/windows-x64/v1"; got != want {
		t.Fatalf("DefaultKeyPrefix() = %q, want %q", got, want)
	}
}

// The owner and the name of a repository may both contain the separator, so a
// flattened slug would hand foo-bar/baz and foo/bar-baz the same cache.
func TestDefaultKeyPrefixKeepsRepositoriesApartInTheSlugFallback(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")

	t.Setenv("GITHUB_REPOSITORY", "foo-bar/baz")
	first := DefaultKeyPrefix()

	t.Setenv("GITHUB_REPOSITORY", "foo/bar-baz")
	second := DefaultKeyPrefix()

	if first == second {
		t.Fatalf("foo-bar/baz and foo/bar-baz share the prefix %q", first)
	}
}

func TestDefaultKeyPrefixStaysWellFormedWithoutRepositoryIdentity(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "ARM64")

	if got, want := DefaultKeyPrefix(), "cache/sccache/unknown/linux-arm64/v1"; got != want {
		t.Fatalf("DefaultKeyPrefix() = %q, want %q", got, want)
	}
}

// path.Join resolves "." and "..", so a component that survived sanitization
// could walk the generated key out of the cache/sccache namespace.
func TestDefaultKeyPrefixCannotEscapeTheNamespace(t *testing.T) {
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")

	for _, repository := range []string{"..", ".", "../..", "owner/.."} {
		t.Run(repository, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY_ID", "")
			t.Setenv("GITHUB_REPOSITORY", repository)

			if got := DefaultKeyPrefix(); !strings.HasPrefix(got, KeyPrefixRoot+"/") {
				t.Fatalf("DefaultKeyPrefix() = %q, want a key under %q", got, KeyPrefixRoot)
			}
		})
	}
}

// The Actions environment and the Go runtime spell the same platform
// differently, so an environment missing RUNNER_ARCH must not land on a second
// prefix for the machine that has it.
func TestDefaultKeyPrefixSpellsThePlatformConsistently(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "42")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	withEnv := DefaultKeyPrefix()

	t.Setenv("RUNNER_OS", "")
	t.Setenv("RUNNER_ARCH", "")
	if got := DefaultKeyPrefix(); got != withEnv && runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		t.Fatalf("DefaultKeyPrefix() = %q without platform env, want %q", got, withEnv)
	}
}

// sccache: s3 recommends sccache: true only where it works: on an agent that
// serves sccache's WebDAV endpoint.
func TestS3RecommendsMagicCacheWhereTheAgentServesIt(t *testing.T) {
	t.Setenv("RUNS_ON_S3_BUCKET_CACHE", "test-cache-bucket")
	t.Setenv("RUNS_ON_AWS_REGION", "us-east-1")
	for _, tc := range []struct {
		name       string
		magicCache bool
		status     int
		recommend  bool
	}{
		{name: "an agent with the WebDAV endpoint", magicCache: true, status: http.StatusUnauthorized, recommend: true},
		{name: "an earlier agent", magicCache: true, status: http.StatusNotFound},
		{name: "no Magic Cache", status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/sccache/" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer agent.Close()
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", agent.URL+"/")
			t.Setenv("GITHUB_ENV", filepath.Join(t.TempDir(), "env"))
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), "s3", tc.magicCache, "")

			if got := strings.Contains(log.String(), "supports sccache: true"); got != tc.recommend {
				t.Errorf("recommends sccache: true: %v, want %v; log:\n%s", got, tc.recommend, log.String())
			}
			if exported, _ := os.ReadFile(os.Getenv("GITHUB_ENV")); !strings.Contains(string(exported), "SCCACHE_BUCKET") {
				t.Errorf("sccache: s3 no longer exports SCCACHE_BUCKET: %q", exported)
			}
		})
	}
}
