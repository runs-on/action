//go:build windows

package stickydisk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

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

func TestCacheMountPreservesExistingBackupPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	backup := target + ".before-stickydisk"
	mountRoot := filepath.Join(root, "sticky")
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "current"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(backup, "user-data")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if _, err := cacheMount(action, mountRoot, target, false); err == nil {
		t.Fatal("cacheMount succeeded despite a colliding backup path")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "keep" {
		t.Fatalf("pre-existing backup data = %q, err = %v", data, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("cold source remains after setup failure: %v", err)
	}
}
