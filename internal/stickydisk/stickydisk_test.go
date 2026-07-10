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
		if !mode.SetupFailureFatal || !mode.PostJobFailureFatal {
			t.Errorf("expected buildkit setup and cleanup contract failures to be fatal")
		}
		if len(mode.Paths) != 0 {
			t.Errorf("expected buildkit mode to have no bind-mount paths, got %v", mode.Paths)
		}
	}
}

func TestDiskStatsThresholds(t *testing.T) {
	gb := uint64(1 << 30)
	tests := []struct {
		name     string
		stats    diskStats
		pressure bool
		critical bool
	}{
		{"plenty free", diskStats{totalBytes: 100 * gb, freeBytes: 50 * gb, totalInodes: 1000, freeInodes: 900}, false, false},
		{"low space", diskStats{totalBytes: 100 * gb, freeBytes: 15 * gb, totalInodes: 1000, freeInodes: 900}, true, false},
		{"critical space", diskStats{totalBytes: 100 * gb, freeBytes: 3 * gb, totalInodes: 1000, freeInodes: 900}, true, true},
		{"low inodes only", diskStats{totalBytes: 100 * gb, freeBytes: 50 * gb, totalInodes: 1000, freeInodes: 80}, true, false},
		{"critical inodes only", diskStats{totalBytes: 100 * gb, freeBytes: 50 * gb, totalInodes: 1000, freeInodes: 30}, true, true},
		{"empty stats", diskStats{}, false, false},
	}
	for _, tt := range tests {
		if got := tt.stats.underPressure(); got != tt.pressure {
			t.Errorf("%s: underPressure() = %v, want %v", tt.name, got, tt.pressure)
		}
		if got := tt.stats.critical(); got != tt.critical {
			t.Errorf("%s: critical() = %v, want %v", tt.name, got, tt.critical)
		}
	}
}

func TestStatDiskSmoke(t *testing.T) {
	stats, err := statDisk(t.TempDir())
	if err != nil {
		t.Fatalf("statDisk failed: %v", err)
	}
	if stats.totalBytes == 0 {
		t.Errorf("expected non-zero totalBytes")
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

func TestModePathsForWindows(t *testing.T) {
	golang := cacheModes["go"]
	linuxPaths := strings.Join(golang.pathsFor("linux"), ",")
	if !strings.Contains(linuxPaths, "~/.cache/go-build") {
		t.Errorf("unexpected linux paths: %s", linuxPaths)
	}
	winPaths := strings.Join(golang.pathsFor("windows"), ",")
	if !strings.Contains(winPaths, "~/AppData/Local/go-build") || !strings.Contains(winPaths, "~/go/pkg/mod") {
		t.Errorf("unexpected windows paths: %s", winPaths)
	}
	// Modes without WindowsPaths keep their default paths on Windows.
	rust := cacheModes["rust"]
	if strings.Join(rust.pathsFor("windows"), ",") != strings.Join(rust.Paths, ",") {
		t.Errorf("rust paths should be identical on windows")
	}
}

func TestModeSupportedOn(t *testing.T) {
	for _, name := range []string{"apt", "buildkit"} {
		if cacheModes[name].supportedOn("windows") {
			t.Errorf("%s must not be supported on windows", name)
		}
		if !cacheModes[name].supportedOn("linux") {
			t.Errorf("%s must be supported on linux", name)
		}
	}
	if !cacheModes["go"].supportedOn("windows") {
		t.Error("go mode must be supported on windows")
	}
}
