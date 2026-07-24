package stickydisk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sethvargo/go-githubactions"
)

func TestParseCacheRequests(t *testing.T) {
	requests, err := ParseCacheRequests([]string{
		"go",
		" NPM ",
		"golang",
		"custom,path=vendor/custom-cache,path=~/.cache/tool",
		"custom,path=vendor/custom-cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected three requests, got %#v", requests)
	}
	if requests[0].Mode.Name != "go" {
		t.Errorf("unexpected go request: %#v", requests[0])
	}
	if requests[1].Mode.Name != "node" {
		t.Errorf("unexpected node request: %#v", requests[1])
	}
	custom := requests[2]
	if !custom.Custom {
		t.Errorf("unexpected custom request: %#v", custom)
	}
	if got, want := strings.Join(custom.Paths, ","), "vendor/custom-cache,~/.cache/tool"; got != want {
		t.Errorf("custom paths = %q, want %q", got, want)
	}
}

func TestParseCacheRequestsAlias(t *testing.T) {
	requests, err := ParseCacheRequests([]string{"buildx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Mode.Name != "buildkit" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestParseCacheRequestsMergesDuplicateBuiltins(t *testing.T) {
	requests, err := ParseCacheRequests([]string{"go", "golang", "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestParseCacheRequestsRejectsInvalidRecords(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{name: "old comma list", entry: "go,node", want: "key=value"},
		{name: "slash options", entry: "buildkit/fail-on-missing=true", want: "slash-delimited"},
		{name: "unknown mode", entry: "bogus", want: "unknown cache mode"},
		{name: "unknown option", entry: "go,missing=true", want: "unknown cache option"},
		{name: "removed fail-on-missing option", entry: "go,fail-on-missing=true", want: "unknown cache option"},
		{name: "empty mode", entry: ",fail-on-missing=true", want: "cache mode is empty"},
		{name: "empty option", entry: "go,", want: "cache option is empty"},
		{name: "custom without path", entry: "custom", want: "requires at least one path"},
		{name: "path on builtin", entry: "go,path=vendor/cache", want: "only supported by the custom"},
		{name: "empty path", entry: "custom,path=", want: "non-empty value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCacheRequests([]string{tt.entry})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseCacheRequests(%q) error = %v, want substring %q", tt.entry, err, tt.want)
			}
		})
	}
}

func TestMissingDiskIsFatal(t *testing.T) {
	t.Setenv("GITHUB_OUTPUT", t.TempDir()+"/output")
	action := githubactions.New()
	err := missing(action, "sticky disk missing")
	if err == nil || !strings.Contains(err.Error(), "sticky disk missing") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := missing(action, "sticky disk was not ready after 10ms"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("cache should fail after timeout: %v", err)
	}
}

func TestWaitForReadyTimesOut(t *testing.T) {
	action := githubactions.New()
	_, err := waitForReady(action, t.TempDir()+"/missing", t.TempDir()+"/unavailable", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "sticky disk was not ready") {
		t.Fatalf("unexpected timeout error: %v", err)
	}
}

func TestWaitForReadyFailsImmediatelyWhenDiskIsUnavailable(t *testing.T) {
	root := t.TempDir()
	unavailableFile := filepath.Join(root, "stickydisk.unavailable")
	if err := os.WriteFile(unavailableFile, []byte("sticky disk unavailable\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	action := githubactions.New()
	started := time.Now()
	_, err := waitForReady(action, filepath.Join(root, "stickydisk.ready"), unavailableFile, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "sticky disk is unavailable") {
		t.Fatalf("unexpected unavailable error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("unavailable marker was not handled immediately: %s", elapsed)
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

func TestValidateCacheDirectoryRejectsFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, ".eslintcache")
	if err := os.WriteFile(file, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateCacheDirectory(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file validation error = %v", err)
	}
	if err := validateCacheDirectory(root); err != nil {
		t.Fatalf("directory validation failed: %v", err)
	}
	if err := validateCacheDirectory(filepath.Join(root, "missing")); err != nil {
		t.Fatalf("missing directory validation failed: %v", err)
	}
}

func TestBuildkitMode(t *testing.T) {
	for _, name := range []string{"buildkit", "buildx"} {
		requests, err := ParseCacheRequests([]string{name})
		if err != nil || len(requests) != 1 {
			t.Fatalf("expected %s to resolve to one mode, got requests=%v err=%v", name, requests, err)
		}
		mode := requests[0].Mode
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
	t.Setenv("XDG_DATA_HOME", "")
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
	pnpm := cacheModes["pnpm"]
	if got := strings.Join(pnpm.pathsFor("linux"), ","); !strings.Contains(got, "~/.local/share/pnpm/store") {
		t.Errorf("unexpected pnpm Linux paths: %s", got)
	}
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
	if got, want := strings.Join(pnpm.pathsFor("linux"), ","), "/tmp/xdg/pnpm/store"; got != want {
		t.Errorf("pnpm XDG path = %s, want %s", got, want)
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

func TestResetBuildkitPreparedState(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "prepared")
	if err := os.WriteFile(stateFile, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}

	requests, err := ParseCacheRequests([]string{"buildkit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := resetBuildkitPreparedState(requests, stateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("prepared state still exists: %v", err)
	}
}
