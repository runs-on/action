package sccache

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureSccacheExportsResolvedPrefix(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", want: "cache/sccache/42/linux-x64/v1"},
		{name: "explicit", input: "/cache/sccache/shared/", want: "cache/sccache/shared"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY_ID", "42")
			t.Setenv("RUNNER_OS", "Linux")
			t.Setenv("RUNNER_ARCH", "X64")
			t.Setenv("RUNS_ON_S3_BUCKET_CACHE", "test-cache-bucket")
			t.Setenv("RUNS_ON_AWS_REGION", "us-east-1")
			t.Setenv("SCCACHE_S3_KEY_PREFIX", "cache/previous-invocation")
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			var log bytes.Buffer
			if err := ConfigureSccache(githubactions.New(githubactions.WithWriter(&log)), "s3", tt.input); err != nil {
				t.Fatal(err)
			}
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
				"SCCACHE_REGION": "us-east-1", "SCCACHE_S3_KEY_PREFIX": tt.want, "RUSTC_WRAPPER": "sccache",
			} {
				if got := exports[key]; got != want {
					t.Errorf("exported %s = %q, want %q", key, got, want)
				}
			}
		})
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

func TestResolveKeyPrefixPrefersExplicitInput(t *testing.T) {
	action := githubactions.New()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "legacy stack-wide prefix", input: "cache/sccache", want: "cache/sccache"},
		{name: "surrounding whitespace", input: "  team/sccache  ", want: "team/sccache"},
		{name: "surrounding slashes", input: "/team/sccache/", want: "team/sccache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveKeyPrefix(action, tc.input); got != tc.want {
				t.Fatalf("ResolveKeyPrefix(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// A prefix outside cache/ is accepted but flagged, because the runner instance
// profile has no S3 access to it and sccache would fail with access denied.
// An explicit prefix is exported through GITHUB_ENV and used as an S3 key, so
// values that cannot survive either are refused rather than passed through.
func TestResolveKeyPrefixRefusesUnusableValues(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "42")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	want := "cache/sccache/42/linux-x64/v1"

	for name, input := range map[string]string{
		"env file delimiter": "cache/x\n_GitHubActionsFileCommandDelimeter_\nAWS_REGION<<_GitHubActionsFileCommandDelimeter_\nattacker",
		"carriage return":    "cache/x\rcache/y",
		"parent component":   "cache/../other",
		"dot component":      "cache/./other",
		"oversized":          "cache/" + strings.Repeat("x", maxKeyPrefixBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if got := ResolveKeyPrefix(githubactions.New(), input); got != want {
				t.Fatalf("ResolveKeyPrefix(%q) = %q, want the default %q", input, got, want)
			}
		})
	}
}

func TestResolveKeyPrefixWarnsOutsideTheCacheNamespace(t *testing.T) {
	for _, tc := range []struct {
		input    string
		wantWarn bool
	}{
		{input: "builds/shared-toolchain", wantWarn: true},
		{input: "cacheable/sccache", wantWarn: true},
		{input: "cache", wantWarn: false},
		{input: "cache/shared-toolchain", wantWarn: false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var out bytes.Buffer
			action := githubactions.New(githubactions.WithWriter(&out))

			if got := ResolveKeyPrefix(action, tc.input); got != tc.input {
				t.Fatalf("ResolveKeyPrefix(%q) = %q, want it unchanged", tc.input, got)
			}
			if warned := strings.Contains(out.String(), "::warning::"); warned != tc.wantWarn {
				t.Fatalf("warning emitted = %t, want %t (output %q)", warned, tc.wantWarn, out.String())
			}
		})
	}
}

func TestResolveKeyPrefixNeverTargetsTheBucketRoot(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY_ID", "123456789")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	action := githubactions.New()

	for _, input := range []string{"", "   ", "/", "///", " / / ", "// //", "\t/\t"} {
		if got, want := ResolveKeyPrefix(action, input), "cache/sccache/123456789/linux-x64/v1"; got != want {
			t.Fatalf("ResolveKeyPrefix(%q) = %q, want %q", input, got, want)
		}
	}
}
