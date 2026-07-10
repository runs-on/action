package stickydisk

import (
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
	action.Warningf("Sticky disk is critically full (%s): resetting all caches on the volume so this and future jobs can run. Consider a larger snap= size.", stats)
	for _, dir := range []string{filepath.Join(mountRoot, "mounts"), filepath.Join(mountRoot, "buildkit", "root"), gitMirrorDir(mountRoot)} {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if err := removeCacheDir(action, dir); err != nil {
			action.Warningf("Failed to reset %s: %v", dir, err)
		}
	}
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
	action.Warningf("Sticky disk is running low on space (%s). The volume is snapshotted as-is: increase the snap= label size (e.g. snap=40gb) or reduce cache usage, otherwise caches will be automatically reset once the volume is critically full.", stats)
	summary := fmt.Sprintf("### :warning: Sticky disk cache almost full\n\n%s\n\n```\n%s\n```\n\nIncrease the `snap=` label size (e.g. `snap=40gb`) or reduce cache usage. Caches are automatically reset when the volume becomes critically full (<%d%% free).\n", stats, breakdown, criticalFreePct)
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
