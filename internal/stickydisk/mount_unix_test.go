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
