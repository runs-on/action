//go:build !windows

package stickydisk

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if err := ensureRealDirectoryPath(mountRoot, src); err != nil {
		return false, fmt.Errorf("validate sticky cache source %s: %w", src, err)
	}
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
		if err := mergeTargetIntoCache(action, target, src, rootOwned); err != nil {
			// The merge is the only step that mutates a warm source; a partial
			// copy (e.g. the disk filling) leaves truncated entries that must
			// not be snapshotted and reported as a hit next job. Cold sources
			// are removed by the deferred cleanup.
			if hit {
				if cleanupErr := removeCacheDir(action, src); cleanupErr != nil {
					err = errors.Join(err, fmt.Errorf("remove partially merged cache source %s: %w", src, cleanupErr))
				}
			}
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

func mountedCacheTargets(mountRoot string) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read active mounts: %w", err)
	}
	return mountedCacheTargetsFromMountInfo(data, mountRoot), nil
}

func mountedCacheTargetsFromMountInfo(data []byte, mountRoot string) []string {
	var targets []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		target := decodeMountInfoPath(fields[4])
		src := filepath.Join(mountRoot, "mounts", sourceDirName(target))
		if sameDirectory(target, src) {
			targets = append(targets, target)
		}
	}
	return targets
}

func decodeMountInfoPath(path string) string {
	return strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	).Replace(path)
}

func mergeTargetIntoCache(action *githubactions.Action, target, src string, rootOwned bool) error {
	var err error
	if rootOwned {
		err = removeRootOwnedMergeConflicts(action, target, src)
	} else {
		err = removeMergeConflicts(target, src)
	}
	if err != nil {
		return fmt.Errorf("prepare cache target %s for merge into %s: %w", target, src, err)
	}

	// GNU cp removes a destination before opening it. Keep this protection on
	// Linux in case the destination changes after the conflict scan.
	copyArgs := []string{"cp", "-a"}
	if runtime.GOOS == "linux" {
		copyArgs = append(copyArgs, "--remove-destination")
	} else {
		copyArgs = append(copyArgs, "-f")
	}
	args := append(copyArgs, "--", strings.TrimRight(target, "/")+"/.", src)
	if rootOwned {
		args = append([]string{"sudo"}, args...)
	}
	if err := runLogged(action, args[0], args[1:]...); err != nil {
		return fmt.Errorf("merge cache target %s into %s: %w", target, src, err)
	}
	return nil
}

// removeMergeConflicts makes the current target authoritative when a restored
// cache has a symlink or a different entry type at the same relative path.
func removeMergeConflicts(target, src string) error {
	return filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == target {
			return nil
		}

		rel, err := filepath.Rel(target, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(src, rel)
		info, err := os.Lstat(destination)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}

		isSymlink := info.Mode()&os.ModeSymlink != 0
		if !isSymlink && entry.IsDir() == info.IsDir() {
			return nil
		}

		return os.RemoveAll(destination)
	})
}

// removeRootOwnedMergeConflicts applies the same conflict policy as
// removeMergeConflicts, but obtains entry types through sudo because apt can
// contain directories that the runner cannot traverse.
func removeRootOwnedMergeConflicts(action *githubactions.Action, target, src string) error {
	targetEntries, err := rootOwnedMergeEntries(target)
	if err != nil {
		return err
	}
	cacheEntries, err := rootOwnedMergeEntries(src)
	if err != nil {
		return err
	}

	for rel, targetType := range targetEntries {
		cacheType, exists := cacheEntries[rel]
		if !exists || cacheType != 'l' && (targetType == 'd') == (cacheType == 'd') {
			continue
		}
		destination := filepath.Join(src, rel)
		if err := runLogged(action, "sudo", "rm", "-rf", "--", destination); err != nil {
			return err
		}
	}
	return nil
}

func rootOwnedMergeEntries(root string) (map[string]byte, error) {
	out, err := exec.Command("sudo", "find", root, "-mindepth", "1", "-printf", `%P\0%y\0`).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list root-owned cache entries under %s: %w: %s", root, err, strings.TrimSpace(string(out)))
	}

	fields := bytes.Split(out, []byte{0})
	if len(fields)%2 != 1 || len(fields[len(fields)-1]) != 0 {
		return nil, fmt.Errorf("parse root-owned cache entries under %s: malformed find output", root)
	}
	entries := make(map[string]byte, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		if len(fields[i]) == 0 || len(fields[i+1]) != 1 {
			return nil, fmt.Errorf("parse root-owned cache entries under %s: malformed find record", root)
		}
		entries[string(fields[i])] = fields[i+1][0]
	}
	return entries, nil
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
