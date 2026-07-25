package stickydisk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
