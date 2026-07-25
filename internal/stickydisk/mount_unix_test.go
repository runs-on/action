//go:build !windows

package stickydisk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestMergeTargetIntoColdCacheRemovesPartialSourceAfterCopyFailure(t *testing.T) {
	bin := t.TempDir()
	cp := `#!/bin/sh
touch "$4/partial"
exit 1
`
	sudo := `#!/bin/sh
shift
exec /bin/rm "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte(cp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mergeTargetIntoCache(githubactions.New(), target, src, false, false); err == nil {
		t.Fatal("mergeTargetIntoCache succeeded with a failing copy")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("partial cache source remains after copy failure: %v", err)
	}
}

func TestMergeTargetIntoWarmCachePreservesSourceAfterCopyFailure(t *testing.T) {
	bin := t.TempDir()
	cp := `#!/bin/sh
touch "$4/partial"
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "cp"), []byte(cp), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(src, "restored")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeTargetIntoCache(githubactions.New(), target, src, false, true); err == nil {
		t.Fatal("mergeTargetIntoCache succeeded with a failing copy")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("restored source was lost after merge failure: %q, err=%v", got, err)
	}
}

func TestMergeTargetIntoWarmCacheReplacesConflictingTypes(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(target, "directory-now"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "directory-now", "current"), []byte("directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file-now"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "file-now"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "file-now", "stale"), []byte("directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "directory-now"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeTargetIntoCache(githubactions.New(), target, src, false, true); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(src, "directory-now", "current")); err != nil || string(got) != "directory" {
		t.Fatalf("current directory did not replace cached file: %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(src, "file-now")); err != nil || string(got) != "file" {
		t.Fatalf("current file did not replace cached directory: %q, err=%v", got, err)
	}
}

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
