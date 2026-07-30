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
