package stickydisk

import (
	"errors"
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

func TestValidateCacheOrdering(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{name: "git with home cache", entries: []string{"git", "go"}},
		{name: "git with absolute custom cache", entries: []string{"git", "custom,path=/opt/cache"}},
		{name: "git with runner home", entries: []string{"git", "custom,path=~"}, wantErr: true},
		{name: "git with tmp", entries: []string{"git", "custom,path=/tmp"}, wantErr: true},
		{name: "git with absolute workspace cache", entries: []string{"git", "custom,path=/home/runner/work/repo/repo/vendor/cache"}, wantErr: true},
		{name: "git with ruby workspace cache", entries: []string{"git", "ruby"}, wantErr: true},
		{name: "git with relative custom cache", entries: []string{"git", "custom,path=vendor/cache"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests, err := ParseCacheRequests(tt.entries)
			if err != nil {
				t.Fatal(err)
			}
			err = validateCacheOrdering(requests, "linux", "/home/runner/work/repo/repo", "/home/runner")
			if tt.wantErr && (err == nil || !strings.Contains(err.Error(), "separate runs-on/action steps") && !strings.Contains(err.Error(), "second runs-on/action step")) {
				t.Fatalf("validation error = %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validation failed: %v", err)
			}
		})
	}
}

func TestValidateCacheOrderingResolvesWorkspaceSymlinks(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(workspace, "vendor", "cache")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "outside-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	requests, err := ParseCacheRequests([]string{"git", "custom,path=" + alias})
	if err != nil {
		t.Fatal(err)
	}
	err = validateCacheOrdering(requests, "linux", workspace, filepath.Join(root, "home"))
	if err == nil || !strings.Contains(err.Error(), "second runs-on/action step") {
		t.Fatalf("symlinked workspace cache ordering error = %v", err)
	}
}

func TestValidateNoOverlappingTargets(t *testing.T) {
	if err := validateNoOverlappingTargets([]string{"/workspace/vendor", "/workspace/vendor/cache"}, "/runs-on/stickydisk", nil); err == nil {
		t.Fatal("nested cache targets were accepted")
	}
	if err := validateNoOverlappingTargets([]string{"/workspace/vendor", "/home/runner/.cache"}, "/runs-on/stickydisk", nil); err != nil {
		t.Fatalf("independent cache targets were rejected: %v", err)
	}
	if err := validateNoOverlappingTargets([]string{"/runs-on"}, "/runs-on/stickydisk", nil); err == nil {
		t.Fatal("cache target containing the sticky mount was accepted")
	}
	if err := validateNoOverlappingTargets([]string{"/runs-on/stickydisk/mounts"}, "/runs-on/stickydisk", nil); err == nil {
		t.Fatal("cache target inside the sticky mount was accepted")
	}
	if err := validateNoOverlappingTargets([]string{"/workspace/vendor"}, "/runs-on/stickydisk", []string{"/workspace/vendor/cache"}); err == nil {
		t.Fatal("cache target overlapping an earlier invocation was accepted")
	}
	if err := validateNoOverlappingTargets([]string{"/workspace/vendor/cache"}, "/runs-on/stickydisk", []string{"/workspace/vendor"}); err == nil {
		t.Fatal("nested cache target overlapping an earlier invocation was accepted")
	}
	if err := validateNoOverlappingTargets([]string{"/workspace/vendor/cache"}, "/runs-on/stickydisk", []string{"/workspace/vendor/cache"}); err != nil {
		t.Fatalf("same cache target from an earlier invocation was rejected: %v", err)
	}
}

func TestValidateNoOverlappingTargetsRejectsActiveSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(active, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := validateNoOverlappingTargets([]string{alias}, filepath.Join(root, "sticky"), []string{active})
	if err == nil || !strings.Contains(err.Error(), "reuse the exact path") {
		t.Fatalf("active target alias error = %v", err)
	}
}

func TestValidateNoOverlappingTargetsResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	vendor := filepath.Join(root, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(vendor, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := validateNoOverlappingTargets([]string{
		filepath.Join(alias, "cache"),
		vendor,
	}, filepath.Join(root, "sticky"), nil)
	if err == nil {
		t.Fatal("symlinked nested cache targets were accepted")
	}
}

func TestActiveMountedTargetsUsesRecordedSource(t *testing.T) {
	mountRoot := t.TempDir()
	target := filepath.Join(t.TempDir(), "workspace", "vendor")
	stale := filepath.Join(t.TempDir(), "stale")
	if err := recordMountedTarget(mountRoot, target); err != nil {
		t.Fatal(err)
	}
	if err := recordMountedTarget(mountRoot, stale); err != nil {
		t.Fatal(err)
	}
	expectedSource := filepath.Join(mountRoot, "mounts", sourceDirName(target))

	active, err := activeMountedTargetsWith(mountRoot, func(actualTarget, actualSource string) bool {
		return actualTarget == target && actualSource == expectedSource
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != target {
		t.Fatalf("active targets = %v, want [%s]", active, target)
	}
}

func TestValidateStickyMount(t *testing.T) {
	root := t.TempDir()
	if err := validateStickyMountWith(root, func(string) bool { return true }); err != nil {
		t.Fatalf("active mount rejected: %v", err)
	}
	if err := validateStickyMountWith(root, func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), "not an active mount") {
		t.Fatalf("inactive mount error = %v", err)
	}
	if err := validateStickyMountWith(filepath.Join(root, "missing"), func(string) bool { return true }); err == nil {
		t.Fatal("missing sticky path was accepted")
	}
}

func TestSameDirectory(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if !sameDirectory(first, first) {
		t.Fatal("same directory was not recognized")
	}
	if sameDirectory(first, second) {
		t.Fatal("different directories were treated as the same")
	}
}

func TestWindowsPartialBackupCleanupKeepsCompleteJunction(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	backup := target + ".before-stickydisk"
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sticky"), []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "removed"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "busy"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("backup is busy")

	err := removeWindowsCacheBackup(githubactions.New(), target, backup, func(path string) error {
		if err := os.Remove(filepath.Join(path, "removed")); err != nil {
			t.Fatal(err)
		}
		return cleanupErr
	})
	if err != nil {
		t.Fatalf("partial backup cleanup failed the active cache: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "sticky"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("complete sticky target marker = %q, err = %v", data, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("reserved backup path remains after cleanup started: %v", err)
	}
	if _, err := os.Stat(backup + ".removing"); err != nil {
		t.Fatalf("partial cleanup path was not retained for retry: %v", err)
	}
}

func TestPostSetupRequiresSuccessfulMounts(t *testing.T) {
	targets := []string{"/var/cache/apt/archives"}
	if postSetupMountsSucceeded(targets, map[string]error{}) {
		t.Fatal("post-setup ran without a mount result")
	}
	if postSetupMountsSucceeded(targets, map[string]error{targets[0]: errors.New("mount failed")}) {
		t.Fatal("post-setup ran after a failed mount")
	}
	if !postSetupMountsSucceeded(targets, map[string]error{targets[0]: nil}) {
		t.Fatal("post-setup did not run after a successful mount")
	}
}

func TestRetryWindowsCacheBackupCleanup(t *testing.T) {
	cleanup := filepath.Join(t.TempDir(), "cache.before-stickydisk.removing")
	if err := os.Mkdir(cleanup, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	retryWindowsCacheBackupCleanup(githubactions.New(), cleanup, func(path string) error {
		called = true
		return os.RemoveAll(path)
	})
	if !called {
		t.Fatal("deferred cleanup was not retried")
	}
	if _, err := os.Stat(cleanup); !os.IsNotExist(err) {
		t.Fatalf("deferred cleanup remains: %v", err)
	}
}

func TestUnsupportedModeMakesCacheResultMiss(t *testing.T) {
	results := []mountResult{
		{Target: "node", Hit: true},
		{Target: "apt", Err: errors.New("not supported on Windows")},
	}
	if allCacheResultsHit(results) {
		t.Fatal("unsupported mode was treated as a cache hit")
	}
}

func TestMergeTargetIntoColdCacheCopiesExistingContents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "nested", "marker"), []byte("seeded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeTargetIntoCache(githubactions.New(), target, src, false, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(src, "nested", "marker"))
	if err != nil || string(data) != "seeded" {
		t.Fatalf("seeded marker = %q, err = %v", data, err)
	}
}

func TestMergeTargetIntoWarmCachePreservesRestoredAndCurrentContents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "restored-only"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "shared"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "current-only"), []byte("checked-in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "shared"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeTargetIntoCache(githubactions.New(), target, src, false, true); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"restored-only": "cached",
		"current-only":  "checked-in",
		"shared":        "current",
	} {
		got, err := os.ReadFile(filepath.Join(src, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, err=%v, want %q", name, got, err, want)
		}
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

func TestWaitForReadyWaitsForFallbackMarker(t *testing.T) {
	root := t.TempDir()
	readyFile := filepath.Join(root, "stickydisk.ready")
	mountRoot := filepath.Join(root, "mount")
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = os.WriteFile(readyFile, []byte(mountRoot), 0o644)
	}()

	got, err := waitForReady(githubactions.New(), readyFile, filepath.Join(root, "unavailable"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != mountRoot {
		t.Fatalf("mount root = %q, want %q", got, mountRoot)
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

func TestAptModeRestoresSystemConfiguration(t *testing.T) {
	mode := cacheModes["apt"]
	if mode.Post == nil || mode.PostJob == nil {
		t.Fatal("expected apt mode to configure and restore system settings")
	}
	if !mode.PostJobFailureFatal {
		t.Fatal("expected apt configuration restore failures to be fatal")
	}
}

func TestGitModeCleanupIsFatal(t *testing.T) {
	mode := cacheModes["git"]
	if mode.Setup == nil || mode.PostJob == nil {
		t.Fatal("expected git mode to define setup and cleanup hooks")
	}
	if !mode.PostJobFailureFatal {
		t.Fatal("expected git proxy cleanup failures to be fatal")
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

func TestResetMountCachesPreservesSourceDirectories(t *testing.T) {
	mountsRoot := filepath.Join(t.TempDir(), "mounts")
	sources := []string{
		filepath.Join(mountsRoot, "go-build"),
		filepath.Join(mountsRoot, "apt"),
	}
	for _, source := range sources {
		if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "nested", "cached"), []byte("cache"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stray := filepath.Join(mountsRoot, "stray")
	if err := os.WriteFile(stray, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetMountCachesWith(mountsRoot, os.RemoveAll); err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		info, err := os.Stat(source)
		if err != nil || !info.IsDir() {
			t.Fatalf("cache source %s was removed: %v", source, err)
		}
		if entries, err := os.ReadDir(source); err != nil || len(entries) != 0 {
			t.Fatalf("cache source %s was not emptied: entries=%v err=%v", source, entries, err)
		}
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray mount entry survived reset: %v", err)
	}
}

func TestResetCacheContentsPreservesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "buildkit", "root")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "cached"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetCacheContentsWith(root, os.RemoveAll); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("cache root was removed: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("cache root was not emptied: entries=%v err=%v", entries, err)
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
