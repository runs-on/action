//go:build windows

package stickydisk

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestRobocopyMergeArgsPreserveLinks(t *testing.T) {
	args := robocopyMergeArgs("target", "source")
	if !slices.Contains(args, "/SJ") {
		t.Fatalf("robocopy args do not preserve junctions: %v", args)
	}
	if !slices.Contains(args, "/SL") {
		t.Fatalf("robocopy args do not preserve symbolic links: %v", args)
	}
	if slices.Contains(args, "/XJ") {
		t.Fatalf("robocopy args still exclude junctions: %v", args)
	}
}

func TestCacheMountRestoresTargetWhenJunctionCreationFails(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	mountRoot := filepath.Join(root, "sticky")
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "cached"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cmd.bat"), []byte("@exit /b 1\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if _, err := cacheMount(action, mountRoot, target, false); err == nil {
		t.Fatal("cacheMount succeeded with a failing mklink command")
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("original target was not restored: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("restored marker = %q, want original", got)
	}
	if _, err := os.Stat(target + ".before-stickydisk"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after successful rollback: %v", err)
	}
}

func TestCacheMountRemovesBackupAfterJunctionCreation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "marker"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	mountRoot := filepath.Join(root, "sticky")
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "cached"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cmd.bat"), []byte("@exit /b 0\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if _, err := cacheMount(action, mountRoot, target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".before-stickydisk"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after successful junction creation: %v", err)
	}
}
