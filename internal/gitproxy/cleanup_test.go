package gitproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCleanupInterruptedRepacks(t *testing.T) {
	root := t.TempDir()
	packDir := filepath.Join(root, "github.com", "owner", "repo.git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}

	temporaryPack := filepath.Join(packDir, "tmp_pack_interrupted")
	repackOutput := filepath.Join(packDir, ".tmp-1234-pack-0123456789012345678901234567890123456789.pack")
	keptPack := filepath.Join(packDir, "pack-reachable.pack")
	for path, content := range map[string]string{
		temporaryPack: "incomplete repack",
		repackOutput:  "new repack output",
		keptPack:      "reachable objects",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outsidePackDir := filepath.Join(root, "scratch", "objects", "pack")
	if err := os.MkdirAll(outsidePackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideTemporaryPack := filepath.Join(outsidePackDir, "tmp_pack_unrelated")
	if err := os.WriteFile(outsideTemporaryPack, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanupInterruptedRepacks(root)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed temporary packs = %d, want 2", removed)
	}
	for _, path := range []string{temporaryPack, repackOutput} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("interrupted pack %s still exists: %v", path, err)
		}
	}
	for _, path := range []string{keptPack, outsideTemporaryPack} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup removed %s: %v", path, err)
		}
	}
}

func TestCleanupInterruptedRepacksAllowsMissingRoot(t *testing.T) {
	removed, err := CleanupInterruptedRepacks(filepath.Join(t.TempDir(), "missing"))
	if err != nil || removed != 0 {
		t.Fatalf("cleanup missing root = (%d, %v), want (0, nil)", removed, err)
	}
}

func TestCleanupInterruptedRepacksRejectsSymlinkedPackDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}

	root := t.TempDir()
	repoObjects := filepath.Join(root, "github.com", "owner", "repo.git", "objects")
	if err := os.MkdirAll(repoObjects, 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePackDir := t.TempDir()
	temporaryPack := filepath.Join(outsidePackDir, "tmp_pack_outside")
	if err := os.WriteFile(temporaryPack, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePackDir, filepath.Join(repoObjects, "pack")); err != nil {
		t.Fatal(err)
	}

	if _, err := CleanupInterruptedRepacks(root); err == nil {
		t.Fatal("symlinked pack directory was accepted")
	}
	if _, err := os.Stat(temporaryPack); err != nil {
		t.Fatalf("cleanup followed symlink outside mirror root: %v", err)
	}
}
