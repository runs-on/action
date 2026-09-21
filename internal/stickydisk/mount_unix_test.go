//go:build !windows

package stickydisk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestCacheMountRemovesColdSourceAfterBindFailure(t *testing.T) {
	bin := t.TempDir()
	sudo := `#!/bin/sh
if [ "$1" = "mount" ]; then
  exit 1
fi
shift
exec /bin/rm "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	mountRoot := filepath.Join(root, "sticky")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(mountRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "seed"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := cacheMount(githubactions.New(), mountRoot, target, false); err == nil {
		t.Fatal("cache mount unexpectedly succeeded")
	}
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("cold source survived bind failure: %v", err)
	}
}

func TestCacheMountRejectsSymlinkedTargetBeforeMerge(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "sticky")
	outside := filepath.Join(root, "outside")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(mountRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "must-not-be-cached")
	if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := cacheMount(githubactions.New(), mountRoot, target, false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked cache target error = %v", err)
	}
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("symlinked target populated sticky source: %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "outside" {
		t.Fatalf("external marker = %q, err=%v", got, err)
	}
}

func TestCacheMountRejectsSymlinkedStickySource(t *testing.T) {
	for _, name := range []string{"mounts parent", "cache source"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			mountRoot := filepath.Join(root, "sticky")
			target := filepath.Join(root, "target")
			outside := t.TempDir()
			if err := os.MkdirAll(mountRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}

			mounts := filepath.Join(mountRoot, "mounts")
			if name == "mounts parent" {
				if err := os.Symlink(outside, mounts); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				if err := os.MkdirAll(mounts, 0o755); err != nil {
					t.Fatal(err)
				}
				src := filepath.Join(mounts, sourceDirName(target))
				if err := os.Symlink(outside, src); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}

			if _, err := cacheMount(githubactions.New(), mountRoot, target, false); err == nil || !strings.Contains(err.Error(), "real directory") {
				t.Fatalf("symlinked sticky source error = %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(outside, "marker")); err != nil || string(got) != "outside" {
				t.Fatalf("external source marker = %q, err = %v", got, err)
			}
		})
	}
}

func TestMountedCacheTargetsFindsEarlierInvocation(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "sticky")
	target := filepath.Join(root, "target with space")
	if err := os.MkdirAll(filepath.Join(mountRoot, "mounts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if err := os.Symlink(target, src); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	escapedTarget := strings.ReplaceAll(target, " ", `\040`)
	mountInfo := []byte(fmt.Sprintf("36 25 0:31 / %s rw,relatime - ext4 /dev/root rw\n", escapedTarget))

	got := mountedCacheTargetsFromMountInfo(mountInfo, mountRoot)
	if len(got) != 1 || got[0] != target {
		t.Fatalf("mounted cache targets = %v, want [%s]", got, target)
	}
}

func TestMergeTargetIntoCacheReplacesDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	src := filepath.Join(root, "src")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{target, src, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "shared"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideMarker := filepath.Join(outside, "must-not-be-written")
	if err := os.WriteFile(outsideMarker, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirect := filepath.Join(src, "shared")
	if err := os.Symlink(outsideMarker, redirect); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := mergeTargetIntoCache(githubactions.New(), target, src, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination symlink survived the merge")
	}
	if got, err := os.ReadFile(redirect); err != nil || string(got) != "current" {
		t.Fatalf("merged destination = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(outsideMarker); err != nil || string(got) != "outside" {
		t.Fatalf("external marker = %q, err = %v", got, err)
	}
}

func TestMergeTargetIntoCacheReplacesTypeConflicts(t *testing.T) {
	for _, test := range []struct {
		name         string
		createTarget func(t *testing.T, path string)
		createCache  func(t *testing.T, path, outside string)
		checkResult  func(t *testing.T, path string)
	}{
		{
			name: "file replaces directory",
			createTarget: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			createCache: func(t *testing.T, path, _ string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "cached"), []byte("cached"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			checkResult: func(t *testing.T, path string) {
				t.Helper()
				if got, err := os.ReadFile(path); err != nil || string(got) != "current" {
					t.Fatalf("merged file = %q, err = %v", got, err)
				}
			},
		},
		{
			name: "directory replaces symlink",
			createTarget: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "current"), []byte("current"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			createCache: func(t *testing.T, path, outside string) {
				t.Helper()
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			checkResult: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !info.IsDir() {
					t.Fatalf("merged destination mode = %v, want directory", info.Mode())
				}
				if got, err := os.ReadFile(filepath.Join(path, "current")); err != nil || string(got) != "current" {
					t.Fatalf("merged directory file = %q, err = %v", got, err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			src := filepath.Join(root, "src")
			outside := filepath.Join(root, "outside")
			for _, dir := range []string{target, src, outside} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			outsideMarker := filepath.Join(outside, "must-not-be-written")
			if err := os.WriteFile(outsideMarker, []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}

			sharedTarget := filepath.Join(target, "shared")
			sharedCache := filepath.Join(src, "shared")
			test.createTarget(t, sharedTarget)
			test.createCache(t, sharedCache, outside)

			if err := mergeTargetIntoCache(githubactions.New(), target, src, false); err != nil {
				t.Fatal(err)
			}
			test.checkResult(t, sharedCache)
			if got, err := os.ReadFile(outsideMarker); err != nil || string(got) != "outside" {
				t.Fatalf("external marker = %q, err = %v", got, err)
			}
		})
	}
}

func TestMergeRootOwnedTargetRemovesConflictWithoutRunnerScan(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	src := filepath.Join(root, "src")
	outside := filepath.Join(root, "outside")
	blocked := filepath.Join(target, "partial")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideMarker := filepath.Join(outside, "must-not-be-written")
	if err := os.WriteFile(outsideMarker, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	redirect := filepath.Join(src, "partial")
	if err := os.Symlink(outside, redirect); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	find := `#!/bin/sh
case "$1" in
  */target) printf 'partial\0d\0' ;;
  */src) printf 'partial\0l\0' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "find"), []byte(find), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := mergeTargetIntoCache(githubactions.New(), target, src, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(redirect); !os.IsNotExist(err) {
		t.Fatalf("root-owned destination conflict survived: %v", err)
	}
	if got, err := os.ReadFile(outsideMarker); err != nil || string(got) != "outside" {
		t.Fatalf("external marker = %q, err = %v", got, err)
	}
}

// A merge that dies partway (e.g. the disk filling) leaves a warm source with
// truncated entries; it must be discarded rather than snapshotted as a hit.
func TestCacheMountDiscardsWarmSourceAfterFailedMerge(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "fresh"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	mountRoot := filepath.Join(root, "sticky")
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "cached"), []byte("warm"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	hit, err := cacheMount(action, mountRoot, target, false)
	if err == nil {
		t.Fatal("cacheMount succeeded with a failing merge")
	}
	if hit {
		t.Fatal("failed merge still reported a cache hit")
	}
	if _, statErr := os.Stat(src); !os.IsNotExist(statErr) {
		t.Fatalf("partially merged warm source survived: %v", statErr)
	}
}
