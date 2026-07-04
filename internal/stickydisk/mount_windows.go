//go:build windows

package stickydisk

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// cacheMount persists target on the sticky disk with an NTFS junction from
// target to a directory on the volume (junctions work across volumes and need
// no admin rights, unlike directory symlinks). The returned hit indicates
// whether the source directory already had content (i.e. was restored from a
// previous snapshot).
//
// Unlike a bind mount, a junction cannot shadow an existing directory: a
// pre-existing target is moved aside (content preserved) before linking.
func cacheMount(action *githubactions.Action, mountRoot, target string, rootOwned bool) (hit bool, err error) {
	_ = rootOwned // no root-owned cache modes on Windows

	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	hit = dirNonEmpty(src)

	if err := os.MkdirAll(src, 0o755); err != nil {
		return hit, fmt.Errorf("failed to create source dir %s: %w", src, err)
	}

	if fi, statErr := os.Lstat(target); statErr == nil {
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			// Stale junction from a previous job on a reused instance (its
			// sticky disk is gone): replace it.
			if err := os.Remove(target); err != nil {
				return hit, fmt.Errorf("failed to remove existing link %s: %w", target, err)
			}
		} else {
			backup := strings.TrimRight(target, `\/`) + ".before-stickydisk"
			_ = os.RemoveAll(backup)
			if err := os.Rename(target, backup); err != nil {
				return hit, fmt.Errorf("failed to move existing %s aside: %w", target, err)
			}
			action.Infof("Moved existing %s to %s", target, backup)
		}
	} else if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return hit, fmt.Errorf("failed to create parent of %s: %w", target, err)
	}

	if err := runLogged(action, "cmd", "/c", "mklink", "/J", target, src); err != nil {
		return hit, err
	}
	return hit, nil
}

// removeCacheDir deletes a cache directory on the sticky disk. Everything on
// the volume is runner-owned on Windows.
func removeCacheDir(_ *githubactions.Action, dir string) error {
	return os.RemoveAll(dir)
}

// duCacheDirs returns a du -sh style breakdown of the given directories,
// computed in-process (no du on Windows).
func duCacheDirs(targets []string) (string, error) {
	var b strings.Builder
	for _, target := range targets {
		fmt.Fprintf(&b, "%s\t%s\n", humanBytes(dirSizeBytes(target)), target)
	}
	return strings.TrimSpace(b.String()), nil
}

func dirSizeBytes(dir string) uint64 {
	var total uint64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
