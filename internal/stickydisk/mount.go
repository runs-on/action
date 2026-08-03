package stickydisk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// resolveTarget expands "~/" against the given home directory and resolves
// relative paths against the workspace directory.
func resolveTarget(target, home, workspace string) string {
	switch {
	case target == "~":
		return home
	case strings.HasPrefix(target, "~/"):
		return filepath.Join(home, target[2:])
	case runtime.GOOS == "windows" && strings.HasPrefix(target, `~\`):
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
	target = canonicalPath(target)
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

// validateWorkspaceAncestry rejects symlinks or junctions between the
// workspace and a workspace-relative cache target. The checkout tree is less
// trusted than the workflow inputs: a committed link such as cache -> $HOME
// would otherwise redirect the merge and mount of custom,path=cache/... into
// an unrelated directory whose contents get persisted into the snapshot.
// Absolute and ~ targets are workflow-authored, so only the final-component
// check in validateCacheDirectory applies to them. Missing ancestors are
// fine: they cannot be links, and cacheMount creates them.
func validateWorkspaceAncestry(workspace, target string) error {
	rel, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	components := strings.Split(rel, string(filepath.Separator))
	current := filepath.Clean(workspace)
	for _, component := range components[:len(components)-1] {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect cache path component %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("cache path component %s is not a real directory; links in the checkout cannot be cache path components", current)
		}
	}
	return nil
}

func validateCacheDirectory(target string) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect cache path %s: %w", target, err)
	}
	if runtime.GOOS != "windows" && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache path %s is a symbolic link; cache targets must be real directories", target)
	}
	// Windows uses junctions for its own active cache targets. Resolve them
	// here; cacheMount separately rejects unrelated live links before merging.
	if runtime.GOOS == "windows" && info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		info, err = os.Stat(target)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect cache path %s: %w", target, err)
		}
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

// ensureRealDirectoryPath creates missing directories below root one component
// at a time and rejects links. Persisted cache state is runner-writable, so
// following a restored link could redirect privileged cache operations off the
// sticky disk.
func ensureRealDirectoryPath(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s is outside sticky disk %s", target, root)
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create cache directory %s: %w", current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect cache directory %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("sticky cache path component %s is not a real directory", current)
		}
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
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
