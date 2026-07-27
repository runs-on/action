package stickydisk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

const mountTargetRecordsDir = ".runs-on-mount-targets"

// resolveTarget expands "~/" against the given home directory and resolves
// relative paths against the workspace directory.
func resolveTarget(target, home, workspace string) string {
	switch {
	case target == "~":
		return home
	case strings.HasPrefix(target, "~/"):
		return filepath.Join(home, target[2:])
	case filepath.IsAbs(target):
		return filepath.Clean(target)
	default:
		return filepath.Join(workspace, target)
	}
}

// sourceDirName returns a readable, collision-safe directory name on the
// sticky disk for the given resolved target path.
func sourceDirName(target string) string {
	sanitized := unsafeChars.ReplaceAllString(strings.Trim(target, "/"), "-")
	if len(sanitized) > 60 {
		sanitized = sanitized[len(sanitized)-60:]
	}
	sum := sha256.Sum256([]byte(target))
	return fmt.Sprintf("%s-%s", strings.Trim(sanitized, "-"), hex.EncodeToString(sum[:])[:8])
}

// dirNonEmpty reports whether the given directory exists and contains entries.
func dirNonEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func validateCacheDirectory(target string) error {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect cache path %s: %w", target, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cache path %s is not a directory; custom file caches are not supported", target)
	}
	return nil
}

func sameDirectory(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}

// liveUnrelatedCacheLink distinguishes an active junction or symbolic link
// from a broken link left behind by a detached volume.
func liveUnrelatedCacheLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

// removeWindowsCacheBackup finalizes the directory-to-junction swap. The
// intact backup is first renamed away from the reserved rollback path. If its
// recursive deletion then fails partway through, the complete sticky junction
// remains active instead of being replaced by a partially deleted backup.
func removeWindowsCacheBackup(action *githubactions.Action, target, backup string, removeBackup func(string) error) error {
	cleanup := backup + ".removing"
	if _, err := os.Lstat(cleanup); err == nil {
		if cleanupErr := removeBackup(cleanup); cleanupErr != nil {
			result := fmt.Errorf("remove stale cache backup cleanup path %s: %w", cleanup, cleanupErr)
			return rollbackWindowsCacheSwap(target, backup, result)
		}
	} else if !os.IsNotExist(err) {
		result := fmt.Errorf("inspect stale cache backup cleanup path %s: %w", cleanup, err)
		return rollbackWindowsCacheSwap(target, backup, result)
	}
	if err := os.Rename(backup, cleanup); err != nil {
		result := fmt.Errorf("move cache backup %s to cleanup path %s: %w", backup, cleanup, err)
		return rollbackWindowsCacheSwap(target, backup, result)
	}

	cleanupErr := removeBackup(cleanup)
	if cleanupErr == nil {
		return nil
	}
	if _, err := os.Lstat(cleanup); os.IsNotExist(err) {
		// Some removal implementations can report a late error after the
		// cleanup path has already disappeared.
		return nil
	} else if err != nil {
		action.Warningf("Could not verify partially removed cache backup %s after cleanup failed: %v (cleanup error: %v). Keeping the complete sticky junction active.", cleanup, err, cleanupErr)
		return nil
	}
	action.Warningf("Could not fully remove cache backup %s: %v. Keeping the complete sticky junction active; the partial cleanup path will be retried by a later invocation.", cleanup, cleanupErr)
	return nil
}

func rollbackWindowsCacheSwap(target, backup string, result error) error {
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return errors.Join(result, fmt.Errorf("remove failed cache junction %s: %w", target, err))
	}
	if err := os.Rename(backup, target); err != nil {
		return errors.Join(result, fmt.Errorf("restore existing cache path %s from %s: %w", target, backup, err))
	}
	return result
}

func retryWindowsCacheBackupCleanup(action *githubactions.Action, cleanup string, remove func(string) error) {
	if err := remove(cleanup); err != nil && !os.IsNotExist(err) {
		action.Warningf("Could not remove deferred cache backup %s: %v. It will be retried by a later action invocation.", cleanup, err)
	}
}

// recordMountedTarget persists the target-to-source mapping on the sticky
// disk. Bind mounts and junctions survive across action invocations in one
// job, so later invocations need this mapping to reject parent/child mounts
// that would hide one another.
func recordMountedTarget(mountRoot, target string) error {
	if !filepath.IsAbs(target) {
		return fmt.Errorf("record sticky cache target %s: path is not absolute", target)
	}
	recordsRoot := filepath.Join(mountRoot, mountTargetRecordsDir)
	if err := os.MkdirAll(recordsRoot, 0o700); err != nil {
		return fmt.Errorf("create sticky cache target records: %w", err)
	}
	record := filepath.Join(recordsRoot, sourceDirName(target))
	if content, err := os.ReadFile(record); err == nil {
		if string(content) != target {
			return fmt.Errorf("sticky cache target record collision for %s", target)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect sticky cache target record %s: %w", record, err)
	}

	temp, err := os.CreateTemp(recordsRoot, ".tmp-")
	if err != nil {
		return fmt.Errorf("create sticky cache target record: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.WriteString(target); err != nil {
		temp.Close()
		return fmt.Errorf("write sticky cache target record %s: %w", target, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close sticky cache target record %s: %w", target, err)
	}
	if err := os.Rename(tempName, record); err != nil {
		return fmt.Errorf("publish sticky cache target record %s: %w", target, err)
	}
	return nil
}

func activeMountedTargets(mountRoot string) ([]string, error) {
	return activeMountedTargetsWith(mountRoot, sameDirectory)
}

func activeMountedTargetsWith(mountRoot string, same func(string, string) bool) ([]string, error) {
	recordsRoot := filepath.Join(mountRoot, mountTargetRecordsDir)
	entries, err := os.ReadDir(recordsRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sticky cache target records: %w", err)
	}

	var active []string
	for _, entry := range entries {
		// Interrupted atomic writes leave only an unpublished temporary file.
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		record := filepath.Join(recordsRoot, entry.Name())
		info, err := os.Lstat(record)
		if err != nil {
			return nil, fmt.Errorf("inspect sticky cache target record %s: %w", record, err)
		}
		if !info.Mode().IsRegular() || info.Size() > 32*1024 {
			return nil, fmt.Errorf("invalid sticky cache target record %s", record)
		}
		content, err := os.ReadFile(record)
		if err != nil {
			return nil, fmt.Errorf("read sticky cache target record %s: %w", record, err)
		}
		target := string(content)
		if !filepath.IsAbs(target) {
			return nil, fmt.Errorf("invalid sticky cache target record %s", record)
		}
		source := filepath.Join(mountRoot, "mounts", entry.Name())
		// Snapshot-restored records are ignored unless the target still
		// resolves to their source in this job.
		if same(target, source) {
			active = append(active, target)
		}
	}
	return active, nil
}

// validateStickyMountWith prevents stale ready markers from redirecting cache
// writes onto the runner's ordinary filesystem. The OS-specific mount check is
// injected in tests because temporary directories are intentionally not mounts.
func validateStickyMountWith(mountRoot string, mounted func(string) bool) error {
	info, err := os.Stat(mountRoot)
	if err != nil {
		return fmt.Errorf("sticky disk path %s is unavailable: %w", mountRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sticky disk path %s is not a directory", mountRoot)
	}
	if !mounted(mountRoot) {
		return fmt.Errorf("sticky disk path %s is not an active mount; the ready marker may be stale", mountRoot)
	}
	return nil
}

func validateStickyMount(mountRoot string) error {
	return validateStickyMountWith(mountRoot, isMountpoint)
}

func runLogged(action *githubactions.Action, name string, args ...string) error {
	action.Infof("Running: %s %s", name, strings.Join(args, " "))
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
