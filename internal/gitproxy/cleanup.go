package gitproxy

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// CleanupInterruptedRepacks removes pack files left under Git's temporary
// name when a background repack is killed. These files are not referenced by
// the repository and can otherwise consume most of a sticky-disk snapshot.
func CleanupInterruptedRepacks(mirrorRoot string) (int, error) {
	info, err := os.Lstat(mirrorRoot)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect mirror root: %w", err)
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return 0, fmt.Errorf("mirror root %s is not a real directory", mirrorRoot)
	}

	root, err := os.OpenRoot(mirrorRoot)
	if err != nil {
		return 0, fmt.Errorf("open mirror root: %w", err)
	}
	defer root.Close()

	removed := 0
	err = fs.WalkDir(root.FS(), ".", func(repoPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect mirror entry %s: %w", repoPath, walkErr)
		}
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
			return nil
		}

		count, err := cleanupInterruptedRepoRepack(root, repoPath)
		removed += count
		if err != nil {
			return err
		}
		return fs.SkipDir
	})
	if err != nil {
		return removed, err
	}
	return removed, nil
}

func cleanupInterruptedRepoRepack(root *os.Root, repoPath string) (int, error) {
	objectsDir := path.Join(repoPath, "objects")
	packDir := path.Join(objectsDir, "pack")
	for _, dir := range []string{objectsDir, packDir} {
		info, err := root.Lstat(dir)
		if os.IsNotExist(err) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("inspect mirror directory %s: %w", dir, err)
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return 0, fmt.Errorf("mirror path %s is not a real directory", dir)
		}
	}

	entries, err := fs.ReadDir(root.FS(), packDir)
	if err != nil {
		return 0, fmt.Errorf("list mirror pack directory %s: %w", packDir, err)
	}
	removed := 0
	for _, entry := range entries {
		if !isInterruptedRepackFile(entry.Name()) {
			continue
		}
		if !entry.Type().IsRegular() {
			return removed, fmt.Errorf("temporary pack %s is not a regular file", path.Join(packDir, entry.Name()))
		}
		if err := root.Remove(path.Join(packDir, entry.Name())); err != nil {
			return removed, fmt.Errorf("remove interrupted repack file %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

func isInterruptedRepackFile(name string) bool {
	for _, prefix := range []string{"tmp_pack_", "tmp_idx_", "tmp_rev_", "tmp_bitmap_", "tmp_mtimes_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if !strings.HasPrefix(name, ".tmp-") {
		return false
	}
	pid, _, found := strings.Cut(strings.TrimPrefix(name, ".tmp-"), "-pack-")
	if !found || pid == "" {
		return false
	}
	for _, digit := range pid {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
