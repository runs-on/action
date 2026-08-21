package sccache

import (
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

	for _, input := range []string{"", "   ", "/", "///"} {
		if got, want := ResolveKeyPrefix(action, input), "cache/sccache/123456789/linux-x64/v1"; got != want {
			t.Fatalf("ResolveKeyPrefix(%q) = %q, want %q", input, got, want)
		}
	}
}
