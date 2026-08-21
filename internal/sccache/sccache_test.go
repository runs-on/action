package sccache

import (
	"runtime"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

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
