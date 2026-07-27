//go:build windows

package stickydisk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

	if err := validateCacheDirectory(target); err != nil {
		return false, err
	}

	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	hit = dirNonEmpty(src)
	if !hit {
		// A cold source populated from checkout/image files must not survive a
		// later setup failure and masquerade as a warm cache in the next job.
		defer func() {
			if err == nil {
				return
			}
			if cleanupErr := removeCacheDir(action, src); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove failed cold cache source %s: %w", src, cleanupErr))
			}
		}()
	}

	if err := os.MkdirAll(src, 0o755); err != nil {
		return hit, fmt.Errorf("failed to create source dir %s: %w", src, err)
	}

	backup := strings.TrimRight(target, `\/`) + ".before-stickydisk"
	if sameDirectory(target, src) {
		action.Infof("Path %s already points at the expected sticky source", target)
		return hit, nil
	}

	liveLink, linkErr := liveUnrelatedCacheLink(target)
	if linkErr != nil {
		return hit, fmt.Errorf("inspect cache link %s: %w", target, linkErr)
	}
	if liveLink {
		return false, fmt.Errorf("cache path %s is already an unrelated junction; expected sticky source %s", target, src)
	}

	// Merge the current target before moving it aside, otherwise checkout or
	// image-provided files—including files added after a warm snapshot—would
	// disappear behind the new junction.
	if dirNonEmpty(target) {
		if err := mergeTargetIntoCache(action, target, src, false); err != nil {
			return false, err
		}
	}

	hasBackup := false
	if fi, statErr := os.Lstat(target); statErr == nil {
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			// A broken junction can only reference a detached prior volume;
			// remove it before linking the current sticky source.
			if err := os.Remove(target); err != nil {
				return hit, fmt.Errorf("failed to remove existing link %s: %w", target, err)
			}
		} else {
			if _, err := os.Lstat(backup); err == nil {
				if err := os.RemoveAll(backup); err != nil {
					return hit, fmt.Errorf("remove stale cache backup %s: %w", backup, err)
				}
			} else if !os.IsNotExist(err) {
				return hit, fmt.Errorf("inspect cache backup path %s: %w", backup, err)
			}
			if err := os.Rename(target, backup); err != nil {
				return hit, fmt.Errorf("failed to move existing %s aside: %w", target, err)
			}
			hasBackup = true
			action.Infof("Moved existing %s to %s", target, backup)
		}
	} else if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return hit, fmt.Errorf("failed to create parent of %s: %w", target, err)
	}

	if err := runLogged(action, "cmd", "/c", "mklink", "/J", target, src); err != nil {
		// A mount failure must not hide the runner image or checkout content
		// that was moved aside to make room for the junction.
		if hasBackup {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return hit, errors.Join(err, fmt.Errorf("restore existing cache path %s from %s: %w", target, backup, restoreErr))
			}
		}
		return hit, err
	}
	if hasBackup {
		if err := os.RemoveAll(backup); err != nil {
			action.Warningf("Could not remove cache backup %s: %v", backup, err)
		}
	}
	return hit, nil
}

// isMountpoint uses mountvol so an ordinary directory on the system drive
// cannot satisfy a stale sticky-disk ready marker.
func isMountpoint(path string) bool {
	mountPath := strings.TrimRight(path, `\/`) + `\`
	return exec.Command("cmd", "/c", "mountvol", mountPath, "/L").Run() == nil
}

func mergeTargetIntoCache(_ *githubactions.Action, target, src string, _ bool) error {
	cmd := exec.Command("robocopy", robocopyMergeArgs(target, src)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// Robocopy exit codes 0-7 are success states (including files copied).
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() <= 7 {
		return nil
	}
	return fmt.Errorf("merge cache target %s into %s: %w: %s", target, src, err, strings.TrimSpace(string(out)))
}

func robocopyMergeArgs(target, src string) []string {
	// Copy junction and symbolic-link entries as links instead of traversing
	// their targets.
	// Excluding them with /XJ would silently drop linked package directories
	// when the original cache target is replaced by the sticky junction.
	return []string{target, src, "/E", "/COPY:DAT", "/DCOPY:DAT", "/SJ", "/SL", "/R:1", "/W:1"}
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
