//go:build !windows

package stickydisk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestSeedColdCacheRemovesPartialSourceAfterCopyFailure(t *testing.T) {
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
	if err := seedColdCache(githubactions.New(), target, src, false); err == nil {
		t.Fatal("seedColdCache succeeded with a failing copy")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("partial cache source remains after copy failure: %v", err)
	}
}
