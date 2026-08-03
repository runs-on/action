//go:build windows

package stickydisk

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

// stubMklink swaps the junction-creation command for the duration of a test.
// Robocopy stays real (it ships on every Windows image); only mklink needs a
// deterministic failure/success seam.
func stubMklink(t *testing.T, fn func(*githubactions.Action, string, string) error) {
	t.Helper()
	prev := runMklinkJunction
	runMklinkJunction = fn
	t.Cleanup(func() { runMklinkJunction = prev })
}

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

func TestCacheMountRejectsJunctionInStickySourcePath(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "sticky")
	outside := filepath.Join(root, "outside")
	target := filepath.Join(root, "target")
	for _, path := range []string{mountRoot, outside, target} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mounts := filepath.Join(mountRoot, "mounts")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", mounts, outside).CombinedOutput(); err != nil {
		t.Skipf("junctions unavailable: %v: %s", err, output)
	}

	if _, err := cacheMount(githubactions.New(), mountRoot, target, false); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("junction-backed sticky source error = %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("cache mount wrote through restored junction: entries=%v err=%v", entries, err)
	}
}

func TestWindowsPathsAreCaseInsensitiveAndExpandBackslashHome(t *testing.T) {
	if canonicalPath(`C:\Cache`) != canonicalPath(`c:\cache`) {
		t.Fatal("Windows path canonicalization is case-sensitive")
	}
	if sourceDirName(`C:\Cache`) != sourceDirName(`c:\cache`) {
		t.Fatal("case variants use different sticky source directories")
	}
	if err := validateNoOverlappingTargets(
		[]string{`C:\Cache`, `c:\cache\pkg`},
		`D:\runs-on\stickydisk`,
	); err == nil {
		t.Fatal("case-variant parent/child cache targets were accepted")
	}
	if got, want := resolveTarget(`~\AppData\Local\tool`, `C:\Users\runner`, `D:\work`), `C:\Users\runner\AppData\Local\tool`; got != want {
		t.Fatalf("backslash home path = %q, want %q", got, want)
	}
}

func TestWindowsActiveCacheTargetsIncludeEarlierInvocation(t *testing.T) {
	t.Setenv(jobCacheStateEnv, `{"mounts":{"C:\\Cache":false},"modes":{"git":true}}`)
	targets, err := activeCacheTargets(`D:\runs-on\stickydisk`)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != `C:\Cache` {
		t.Fatalf("active cache targets = %v", targets)
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

	stubMklink(t, func(*githubactions.Action, string, string) error {
		return errors.New("mklink failed")
	})

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

	stubMklink(t, func(_ *githubactions.Action, target, _ string) error {
		return os.MkdirAll(target, 0o755)
	})

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if _, err := cacheMount(action, mountRoot, target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".before-stickydisk"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after successful junction creation: %v", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("junction stub did not create target: %v", err)
	}
}

// A pre-existing path at the backup location is user or checkout content
// (runners are ephemeral, and every failure path cleans its own backup);
// it must be rejected, never deleted.
func TestCacheMountRejectsPreexistingBackupPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "marker"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := target + ".before-stickydisk"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "user-file"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}

	mountRoot := filepath.Join(root, "sticky")
	if err := os.MkdirAll(filepath.Join(mountRoot, "mounts"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubMklink(t, func(*githubactions.Action, string, string) error {
		t.Fatal("mklink must not run when the backup path is occupied")
		return nil
	})

	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if _, err := cacheMount(action, mountRoot, target, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("pre-existing backup path error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backup, "user-file")); err != nil || string(got) != "user" {
		t.Fatalf("user content at backup path was modified: %q, err = %v", got, err)
	}
}
