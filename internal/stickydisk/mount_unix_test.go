//go:build !windows

package stickydisk

import (
	"os"
	"path/filepath"
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
