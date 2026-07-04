//go:build !windows

package stickydisk

import (
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
	src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
	hit = dirNonEmpty(src)

	if isMountpoint(target) {
		action.Warningf("Path %s is already a mountpoint, skipping", target)
		return hit, nil
	}

	// The sticky disk root is owned by the runner user, so no sudo needed here.
	if err := os.MkdirAll(src, 0755); err != nil {
		return hit, fmt.Errorf("failed to create source dir %s: %w", src, err)
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
