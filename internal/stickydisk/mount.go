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

func runLogged(action *githubactions.Action, name string, args ...string) error {
	action.Infof("Running: %s %s", name, strings.Join(args, " "))
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
