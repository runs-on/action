//go:build !windows

package stickydisk

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// isMountpoint reports whether the given path is already a mountpoint.
func isMountpoint(path string) bool {
	return exec.Command("mountpoint", "-q", path).Run() == nil
}

// cacheMount persists target on the sticky disk by bind-mounting a
// runner-owned (or root-owned) directory from the volume onto it.
// The returned hit indicates whether the source directory already had content
// (i.e. was restored from a previous snapshot).
func cacheMount(action *githubactions.Action, mountRoot, target string, rootOwned bool) (hit bool, err error) {
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

	// The sticky disk root is owned by the runner user, so no sudo needed here.
	if err := os.MkdirAll(src, 0755); err != nil {
		return hit, fmt.Errorf("failed to create source dir %s: %w", src, err)
	}

	if isMountpoint(target) {
		if sameDirectory(target, src) {
			action.Infof("Path %s is already mounted from the expected sticky source", target)
			return hit, nil
		}
		return false, fmt.Errorf("cache path %s is already an unrelated mountpoint; expected sticky source %s", target, src)
	}

	// Merge files already created by checkout or the runner image before the
	// bind mount hides the target. This also keeps newly checked-in files when
	// a warm snapshot contains an older version of a workspace cache.
	if dirNonEmpty(target) {
		if err := mergeTargetIntoCache(action, target, src, rootOwned, hit); err != nil {
			return false, err
		}
	}
	if rootOwned {
		if err := runLogged(action, "sudo", "chown", "root:root", src); err != nil {
			return hit, err
		}
	}

	// Create the target directory. Try without sudo first (home/workspace
	// paths), falling back to sudo for system paths. Track the topmost
	// missing ancestor so ownership can be fixed on everything we create.
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		firstMissing := target
		for {
			parent := filepath.Dir(firstMissing)
			if parent == firstMissing {
				break
			}
			if _, err := os.Stat(parent); err == nil {
				break
			}
			firstMissing = parent
		}
		if err := os.MkdirAll(target, 0755); err != nil {
			if err := runLogged(action, "sudo", "mkdir", "-p", target); err != nil {
				return hit, err
			}
			if !rootOwned {
				owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
				if err := runLogged(action, "sudo", "chown", "-R", owner, firstMissing); err != nil {
					return hit, err
				}
			}
		}
	}

	if err := runLogged(action, "sudo", "mount", "--bind", src, target); err != nil {
		return hit, err
	}

	return hit, nil
}

func mergeTargetIntoCache(action *githubactions.Action, target, src string, rootOwned, restored bool) error {
	if err := removeMergeTypeConflicts(action, target, src, rootOwned); err != nil {
		return err
	}
	args := []string{"cp", "-a", "--", strings.TrimRight(target, "/") + "/.", src}
	if rootOwned {
		args = append([]string{"sudo"}, args...)
	}
	if err := runLogged(action, args[0], args[1:]...); err != nil {
		copyErr := fmt.Errorf("merge cache target %s into %s: %w", target, src, err)
		if restored {
			return copyErr
		}
		// A later job uses a non-empty source as its hit signal, so a partial
		// cold copy must never survive and masquerade as a restorable cache.
		if cleanupErr := removeCacheDir(action, src); cleanupErr != nil {
			return errors.Join(copyErr, fmt.Errorf("remove partial cache source %s: %w", src, cleanupErr))
		}
		return copyErr
	}
	return nil
}

// removeMergeTypeConflicts makes the current checkout authoritative when a
// restored entry changed between directory, regular file, or symlink. cp -a
// cannot replace a destination directory with a file (or the reverse).
func removeMergeTypeConflicts(action *githubactions.Action, target, src string, rootOwned bool) error {
	return filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect cache target %s: %w", path, walkErr)
		}
		rel, err := filepath.Rel(target, path)
		if err != nil {
			return fmt.Errorf("resolve cache target entry %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		destination := filepath.Join(src, rel)
		destinationInfo, err := os.Lstat(destination)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect restored cache entry %s: %w", destination, err)
		}
		sourceInfo, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect current cache entry %s: %w", path, err)
		}
		if sourceInfo.Mode().Type() == destinationInfo.Mode().Type() {
			return nil
		}

		// Root-owned modes such as apt require elevated removal; ordinary
		// workspace and home caches stay within runner-user permissions.
		if rootOwned {
			if err := removeCacheDir(action, destination); err != nil {
				return fmt.Errorf("replace conflicting restored cache entry %s: %w", destination, err)
			}
			return nil
		}
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("replace conflicting restored cache entry %s: %w", destination, err)
		}
		return nil
	})
}

// removeCacheDir deletes a cache directory on the sticky disk. Contents may be
// root-owned (apt mode), hence sudo.
func removeCacheDir(action *githubactions.Action, dir string) error {
	return runLogged(action, "sudo", "rm", "-rf", dir)
}

// duCacheDirs returns a du -sh breakdown of the given directories. Some
// contents may be root-owned (apt mode), hence sudo.
func duCacheDirs(targets []string) (string, error) {
	out, err := exec.Command("sudo", append([]string{"du", "-sh"}, targets...)...).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
