package stickydisk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sethvargo/go-githubactions"
)

const (
	warnFreePct          = 20
	criticalFreePct      = 5
	warnFreeInodePct     = 10
	criticalFreeInodePct = 5
)

type diskStats struct {
	totalBytes  uint64
	freeBytes   uint64
	totalInodes uint64
	freeInodes  uint64
}

func (d diskStats) freePct() float64 {
	if d.totalBytes == 0 {
		return 100
	}
	return float64(d.freeBytes) / float64(d.totalBytes) * 100
}

func (d diskStats) freeInodePct() float64 {
	if d.totalInodes == 0 {
		return 100
	}
	return float64(d.freeInodes) / float64(d.totalInodes) * 100
}

func (d diskStats) critical() bool {
	return d.freePct() < criticalFreePct || d.freeInodePct() < criticalFreeInodePct
}

func (d diskStats) underPressure() bool {
	return d.freePct() < warnFreePct || d.freeInodePct() < warnFreeInodePct
}

func (d diskStats) String() string {
	usedGB := float64(d.totalBytes-d.freeBytes) / (1 << 30)
	totalGB := float64(d.totalBytes) / (1 << 30)
	return fmt.Sprintf("%.1f/%.1fGB used, %.1f%% free, %.1f%% inodes free", usedGB, totalGB, d.freePct(), d.freeInodePct())
}

// checkCritical resets all caches on the sticky disk when the volume is
// critically full (space or inodes). Restoring a snapshot of a ~full volume
// would otherwise make every subsequent build fail with ENOSPC: wiping the
// caches lets this job run cold and the next snapshot start clean.
// Must run before cache-hit detection so wiped modes report a miss.
func checkCritical(action *githubactions.Action, mountRoot string) {
	stats, err := statDisk(mountRoot)
	if err != nil {
		action.Warningf("Could not check sticky disk usage: %v", err)
		return
	}
	if !stats.critical() {
		return
	}
	action.Warningf("Sticky disk is critically full (%s): resetting all caches on the volume so this and future jobs can run. Consider a larger sticky= size.", stats)
	gitSafe := true
	if _, err := os.Stat(gitProxyStateFile); err == nil {
		if err := stopGit(action); err != nil {
			gitSafe = false
			action.Warningf("Failed to stop active git cache before pressure reset: %v", err)
		}
	}
	buildkitRoot := filepath.Join(mountRoot, "buildkit", "root")
	preserveBuildkitRoot := false
	if _, err := os.Stat(buildkitPreparedStateFile); err == nil {
		// A prior action invocation may already have created the builder. Stop
		// its container and empty the backing directory in place, preserving
		// the builder metadata and volume so later build steps still work.
		preserveBuildkitRoot = true
		safeToReset, pauseErr := pauseBuildkitForPressureReset(action, buildkitRoot)
		if pauseErr != nil {
			action.Warningf("Failed to pause active BuildKit cache before pressure reset: %v", pauseErr)
		} else if safeToReset {
			if err := resetCacheContents(action, buildkitRoot); err != nil {
				action.Warningf("Failed to reset active BuildKit cache: %v", err)
			}
		}
	} else if !os.IsNotExist(err) {
		// An unreadable marker means BuildKit may still own the directory.
		preserveBuildkitRoot = true
		action.Warningf("Could not inspect active BuildKit cache before pressure reset: %v", err)
	}

	// Cache source directories may already be bind-mounted by an earlier
	// action invocation in this job. Empty their contents while preserving
	// the directory inode so active targets remain attached to a path that
	// will be included in the sticky-disk snapshot.
	if err := resetMountCaches(action, filepath.Join(mountRoot, "mounts")); err != nil {
		action.Warningf("Failed to reset mounted caches: %v", err)
	}

	for _, dir := range []string{buildkitRoot, gitMirrorDir(mountRoot)} {
		if dir == gitMirrorDir(mountRoot) && !gitSafe {
			continue
		}
		if dir == buildkitRoot && preserveBuildkitRoot {
			continue
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if err := removeCacheDir(action, dir); err != nil {
			action.Warningf("Failed to reset %s: %v", dir, err)
		}
	}
}

func resetCacheContents(action *githubactions.Action, root string) error {
	return resetCacheContentsWith(root, func(path string) error {
		return removeCacheDir(action, path)
	})
}

// resetCacheContentsWith removes every child while preserving the root inode.
// Active bind mounts and Docker local volumes keep referring to that inode.
func resetCacheContentsWith(root string, remove func(string) error) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cache root %s: %w", root, err)
	}

	var resetErr error
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if err := remove(path); err != nil {
			resetErr = errors.Join(resetErr, fmt.Errorf("remove cache entry %s: %w", path, err))
		}
	}
	return resetErr
}

func resetMountCaches(action *githubactions.Action, mountsRoot string) error {
	return resetMountCachesWith(mountsRoot, func(path string) error {
		return removeCacheDir(action, path)
	})
}

// resetMountCachesWith preserves every top-level cache source directory while
// removing its children. Bind mounts reference the source inode, so removing
// the source itself would detach subsequent writes from the snapshot tree.
func resetMountCachesWith(mountsRoot string, remove func(string) error) error {
	entries, err := os.ReadDir(mountsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read mounted cache root %s: %w", mountsRoot, err)
	}

	var resetErr error
	for _, entry := range entries {
		source := filepath.Join(mountsRoot, entry.Name())
		if !entry.IsDir() {
			if err := remove(source); err != nil {
				resetErr = errors.Join(resetErr, fmt.Errorf("remove stale mount entry %s: %w", source, err))
			}
			continue
		}
		children, err := os.ReadDir(source)
		if err != nil {
			resetErr = errors.Join(resetErr, fmt.Errorf("read cache source %s: %w", source, err))
			continue
		}
		for _, child := range children {
			path := filepath.Join(source, child.Name())
			if err := remove(path); err != nil {
				resetErr = errors.Join(resetErr, fmt.Errorf("remove cache entry %s: %w", path, err))
			}
		}
	}
	return resetErr
}

// warnOnPressure logs a usage summary in the post step and emits a warning
// plus a job summary when the volume is running low on space or inodes.
func warnOnPressure(action *githubactions.Action, mountRoot string) {
	stats, err := statDisk(mountRoot)
	if err != nil {
		action.Warningf("Could not check sticky disk usage: %v", err)
		return
	}
	action.Infof("Sticky disk usage: %s", stats)
	if !stats.underPressure() {
		return
	}

	breakdown := usageBreakdown(mountRoot)
	action.Warningf("Sticky disk is running low on space (%s). The volume is snapshotted as-is: increase the sticky= label size (e.g. sticky=40gb) or reduce cache usage, otherwise caches will be automatically reset once the volume is critically full.", stats)
	summary := fmt.Sprintf("### :warning: Sticky disk cache almost full\n\n%s\n\n```\n%s\n```\n\nIncrease the `sticky=` label size (e.g. `sticky=40gb`) or reduce cache usage. Caches are automatically reset when the volume becomes critically full (<%d%% free).\n", stats, breakdown, criticalFreePct)
	action.AddStepSummary(summary)
}

// usageBreakdown returns a du -sh style breakdown of the cache directories.
func usageBreakdown(mountRoot string) string {
	var targets []string
	if entries, err := os.ReadDir(filepath.Join(mountRoot, "mounts")); err == nil {
		for _, entry := range entries {
			targets = append(targets, filepath.Join(mountRoot, "mounts", entry.Name()))
		}
	}
	if _, err := os.Stat(filepath.Join(mountRoot, "buildkit", "root")); err == nil {
		targets = append(targets, filepath.Join(mountRoot, "buildkit", "root"))
	}
	if _, err := os.Stat(gitMirrorDir(mountRoot)); err == nil {
		targets = append(targets, gitMirrorDir(mountRoot))
	}
	if len(targets) == 0 {
		return "(no cache directories)"
	}
	out, err := duCacheDirs(targets)
	if err != nil {
		return fmt.Sprintf("(du failed: %v)", err)
	}
	return out
}
