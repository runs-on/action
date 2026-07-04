package stickydisk

import (
	"strings"
	"testing"
)

func TestResolveModes(t *testing.T) {
	modes, unknown := ResolveModes([]string{"go", "NPM", " ruby ", "bogus", "golang", ""})
	if len(unknown) != 1 || unknown[0] != "bogus" {
		t.Errorf("expected unknown [bogus], got %v", unknown)
	}
	var names []string
	for _, m := range modes {
		names = append(names, m.Name)
	}
	// golang is an alias of go, already seen: deduplicated
	expected := []string{"go", "node", "ruby"}
	if strings.Join(names, ",") != strings.Join(expected, ",") {
		t.Errorf("expected modes %v, got %v", expected, names)
	}
}

func TestResolveTarget(t *testing.T) {
	home := "/home/runner"
	workspace := "/home/runner/work/repo/repo"
	tests := []struct {
		input    string
		expected string
	}{
		{"~/.npm", "/home/runner/.npm"},
		{"~", "/home/runner"},
		{"/var/cache/apt/archives", "/var/cache/apt/archives"},
		{"vendor/bundle", "/home/runner/work/repo/repo/vendor/bundle"},
		{"~/go/pkg/mod", "/home/runner/go/pkg/mod"},
	}
	for _, tt := range tests {
		if got := resolveTarget(tt.input, home, workspace); got != tt.expected {
			t.Errorf("resolveTarget(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSourceDirName(t *testing.T) {
	a := sourceDirName("/home/runner/.cache/go-build")
	b := sourceDirName("/home/runner/.cache/go_build")
	if a == b {
		t.Errorf("expected distinct names for distinct paths, got %q for both", a)
	}
	if !strings.HasPrefix(a, "home-runner-.cache-go-build-") {
		t.Errorf("expected readable prefix, got %q", a)
	}
	if a != sourceDirName("/home/runner/.cache/go-build") {
		t.Errorf("expected deterministic name")
	}
	// long paths keep a bounded name
	long := sourceDirName("/" + strings.Repeat("a/", 100) + "leaf")
	if len(long) > 70 {
		t.Errorf("expected bounded length, got %d chars: %q", len(long), long)
	}
}

func TestBuildkitMode(t *testing.T) {
	for _, name := range []string{"buildkit", "buildx"} {
		modes, unknown := ResolveModes([]string{name})
		if len(unknown) != 0 || len(modes) != 1 {
			t.Fatalf("expected %s to resolve to one mode, got modes=%v unknown=%v", name, modes, unknown)
		}
		mode := modes[0]
		if mode.Name != "buildkit" {
			t.Errorf("expected canonical name buildkit, got %s", mode.Name)
		}
		if mode.Setup == nil || mode.PostJob == nil {
			t.Errorf("expected buildkit mode to define Setup and PostJob")
		}
		if len(mode.Paths) != 0 {
			t.Errorf("expected buildkit mode to have no bind-mount paths, got %v", mode.Paths)
		}
	}
}

func TestBuildkitTarballURL(t *testing.T) {
	url := buildkitTarballURL("v0.31.1", "arm64")
	expected := "https://github.com/moby/buildkit/releases/download/v0.31.1/buildkit-v0.31.1.linux-arm64.tar.gz"
	if url != expected {
		t.Errorf("got %q, want %q", url, expected)
	}
}

func TestValidModesContainsCore(t *testing.T) {
	valid := strings.Join(ValidModes(), ",")
	for _, name := range []string{"go", "node", "ruby", "rust", "python", "apt"} {
		if !strings.Contains(valid, name) {
			t.Errorf("expected %s in valid modes, got %s", name, valid)
		}
	}
}
