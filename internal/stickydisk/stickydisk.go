package stickydisk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

const (
	defaultWaitTimeout = 5 * time.Minute
	readyPollInterval  = 500 * time.Millisecond
)

// defaultReadyFile mirrors the agent's STICKYDISK_READY_FILE; only a fallback,
// the agent publishes RUNS_ON_STICKYDISK_READY_FILE.
func defaultReadyFile() string {
	if runtime.GOOS == "windows" {
		return `C:\runs-on\stickydisk.ready`
	}
	return "/runs-on/stickydisk.ready"
}

// defaultUnavailableFile mirrors the agent fallback path; normally the agent
// publishes RUNS_ON_STICKYDISK_UNAVAILABLE_FILE in the runner environment.
func defaultUnavailableFile() string {
	if runtime.GOOS == "windows" {
		return `C:\runs-on\stickydisk.unavailable`
	}
	return "/runs-on/stickydisk.unavailable"
}

func supportedOS() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

// Options configures the sticky disk cache setup.
type Options struct {
	// StickyCache is the newline-delimited list of sticky_cache records.
	StickyCache []string
	// StickyWaitTimeout bounds how long to wait for the sticky disk to be ready.
	StickyWaitTimeout time.Duration
}

type mountResult struct {
	Target string
	Hit    bool
	Err    error
}

// Configure bind-mounts the requested cache directories onto the job's sticky
// disk. It requires a `sticky=[<name>:]<size>` label on the job. Mount failures
// are reported as warnings. A missing or unready sticky disk returns an error
// because sticky_cache explicitly requires persistent storage.
func Configure(action *githubactions.Action, opts Options) error {
	requests, err := ParseCacheRequests(opts.StickyCache)
	if err != nil {
		return err
	}

	if !supportedOS() {
		action.Warningf("Sticky disk cache is only supported on Linux and Windows runners, skipping.")
		action.SetOutput("cache-hit", "false")
		return nil
	}

	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		if runtime.GOOS == "windows" {
			home = `C:\Users\runner`
		} else {
			home = "/home/runner"
		}
	}
	if err := validateCacheOrdering(requests, runtime.GOOS, workspace); err != nil {
		return err
	}

	readyFile := os.Getenv("RUNS_ON_STICKYDISK_READY_FILE")
	if readyFile == "" {
		readyFile = defaultReadyFile()
	}
	unavailableFile := os.Getenv("RUNS_ON_STICKYDISK_UNAVAILABLE_FILE")
	if unavailableFile == "" {
		unavailableFile = defaultUnavailableFile()
	}

	if os.Getenv("RUNS_ON_STICKYDISK_DIR") == "" {
		if _, err := os.Stat(unavailableFile); err == nil {
			return missing(action, fmt.Sprintf("sticky disk is unavailable (marker: %s)", unavailableFile))
		}
		return missing(action, "sticky disk is unavailable: RUNS_ON_STICKYDISK_DIR is not set")
	}

	timeout := opts.StickyWaitTimeout
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	mountRoot, err := waitForReady(action, readyFile, unavailableFile, timeout)
	if err != nil {
		return missing(action, err.Error())
	}
	if err := validateStickyMount(mountRoot); err != nil {
		return missing(action, err.Error())
	}
	action.Infof("Sticky disk ready at %s (name: %s)", mountRoot, os.Getenv("RUNS_ON_STICKYDISK_NAME"))

	// Self-heal a critically full volume before cache-hit detection: wiped
	// caches report a miss and the next snapshot starts clean.
	checkCritical(action, mountRoot)

	// Resolve all targets, deduplicating by absolute path.
	type mountSpec struct {
		target string
		root   bool
	}
	type postSetup struct {
		name    string
		targets []string
		run     func(action *githubactions.Action) error
	}
	var specs []mountSpec
	var posts []postSetup
	var results []mountResult
	seen := map[string]bool{}
	seenPosts := map[string]bool{}
	addTarget := func(path string, root bool) string {
		resolved := resolveTarget(path, home, workspace)
		if seen[resolved] {
			return resolved
		}
		seen[resolved] = true
		specs = append(specs, mountSpec{target: resolved, root: root})
		return resolved
	}
	for _, request := range requests {
		if request.Custom {
			for _, path := range request.Paths {
				addTarget(path, false)
			}
			continue
		}
		mode := request.Mode
		if !mode.supportedOn(runtime.GOOS) {
			action.Warningf("Cache mode '%s' is not supported on Windows runners, skipping.", mode.Name)
			results = append(results, mountResult{
				Target: mode.Name,
				Err:    fmt.Errorf("cache mode %s is not supported on Windows", mode.Name),
			})
			continue
		}
		// Modes with a Setup function own their whole setup (no bind mounts).
		if mode.Setup != nil {
			hit, err := mode.Setup(action, mountRoot)
			if err != nil {
				if mode.SetupFailureFatal {
					return fmt.Errorf("failed to set up %s cache: %w", mode.Name, err)
				}
				action.Warningf("Failed to set up %s cache: %v", mode.Name, err)
			}
			results = append(results, mountResult{Target: mode.Name, Hit: hit && err == nil, Err: err})
			continue
		}
		var modeTargets []string
		for _, path := range mode.pathsFor(runtime.GOOS) {
			modeTargets = append(modeTargets, addTarget(path, mode.Root))
		}
		// A mode can be listed more than once, but its process-wide setup and
		// saved post-job state must be established only once per invocation.
		if mode.Post != nil && !seenPosts[mode.Name] {
			posts = append(posts, postSetup{name: mode.Name, targets: modeTargets, run: mode.Post})
			seenPosts[mode.Name] = true
		}
	}
	targets := make([]string, 0, len(specs))
	for _, spec := range specs {
		targets = append(targets, spec.target)
	}
	if err := validateNoOverlappingTargets(targets, mountRoot); err != nil {
		return err
	}
	if len(specs) == 0 && len(results) == 0 {
		action.Warningf("No cache paths to set up.")
		action.SetOutput("cache-hit", "false")
		return nil
	}

	mountErrors := make(map[string]error, len(specs))
	for _, spec := range specs {
		hit, err := cacheMount(action, mountRoot, spec.target, spec.root)
		if err != nil {
			action.Warningf("Failed to set up cache for %s: %v", spec.target, err)
		}
		results = append(results, mountResult{Target: spec.target, Hit: hit && err == nil, Err: err})
		mountErrors[spec.target] = err
	}

	for _, post := range posts {
		if !postSetupMountsSucceeded(post.targets, mountErrors) {
			action.Warningf("Skipping %s cache post-setup because a required cache mount failed.", post.name)
			continue
		}
		if err := post.run(action); err != nil {
			action.Warningf("Cache post-setup step failed: %v", err)
		}
	}

	allHit := allCacheResultsHit(results)
	action.Infof("Sticky disk cache summary:")
	for _, res := range results {
		status := "restored"
		if res.Err != nil {
			status = "error"
		} else if !res.Hit {
			status = "empty"
		}
		action.Infof("  %-10s %s", status, res.Target)
	}
	action.SetOutput("cache-hit", strconv.FormatBool(allHit))
	return nil
}

func validateNoOverlappingTargets(targets []string, mountRoot string) error {
	canonical := make([]string, len(targets))
	for i, target := range targets {
		canonical[i] = filepath.Clean(target)
	}
	canonicalMountRoot := filepath.Clean(mountRoot)
	for i, target := range canonical {
		// cacheMount creates the source below mountRoot before copying the
		// target. Either containment direction would copy the sticky disk into
		// itself or place the source below the directory being copied.
		if pathContains(target, canonicalMountRoot) || pathContains(canonicalMountRoot, target) {
			return fmt.Errorf("sticky cache path %s overlaps the sticky disk mount %s", targets[i], mountRoot)
		}
	}
	for i := range canonical {
		for j := i + 1; j < len(canonical); j++ {
			if pathContains(canonical[i], canonical[j]) || pathContains(canonical[j], canonical[i]) {
				return fmt.Errorf("sticky cache paths overlap: %s and %s; combine them into one cache target", targets[i], targets[j])
			}
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func allCacheResultsHit(results []mountResult) bool {
	for _, result := range results {
		if !result.Hit {
			return false
		}
	}
	return true
}

func postSetupMountsSucceeded(targets []string, mountErrors map[string]error) bool {
	for _, target := range targets {
		err, found := mountErrors[target]
		if !found || err != nil {
			return false
		}
	}
	return true
}

func validateCacheOrdering(requests []CacheRequest, goos, workspace string) error {
	if goos == "windows" {
		return nil
	}

	hasGit := false
	for _, request := range requests {
		if !request.Custom {
			switch request.Mode.Name {
			case "git":
				hasGit = true
			}
		}
	}
	if !hasGit {
		return nil
	}

	var workspaceModes []string
	for _, request := range requests {
		if !request.Custom && request.Mode.Name == "git" {
			continue
		}
		paths := request.Paths
		name := "custom"
		if !request.Custom {
			name = request.Mode.Name
			paths = request.Mode.pathsFor(goos)
		}
		for _, path := range paths {
			inWorkspace, err := isWorkspaceCachePath(path, workspace)
			if err != nil {
				return fmt.Errorf("classify %s cache path %s: %w", name, path, err)
			}
			if inWorkspace {
				workspaceModes = append(workspaceModes, name)
			}
		}
	}

	if len(workspaceModes) > 0 {
		return fmt.Errorf("git cache mode cannot be combined with workspace-relative cache modes (%s) in one invocation; run git before checkout, then run workspace-relative caches in a second runs-on/action step after checkout", strings.Join(workspaceModes, ", "))
	}
	return nil
}

func isWorkspaceCachePath(path, workspace string) (bool, error) {
	if !filepath.IsAbs(path) {
		return path != "~" && !strings.HasPrefix(path, "~/"), nil
	}
	if workspace == "" {
		return false, nil
	}
	return pathContains(filepath.Clean(workspace), filepath.Clean(path)), nil
}

// missing handles the no-sticky-disk case. Requesting sticky_cache is an
// explicit persistence contract, so absence or readiness timeout is fatal.
func missing(action *githubactions.Action, msg string) error {
	action.SetOutput("cache-hit", "false")
	return errors.New(msg)
}

// waitForReady polls until the agent publishes either the mounted-ready marker
// or the terminal-unavailable marker, bounded by timeout.
func waitForReady(action *githubactions.Action, readyFile string, unavailableFile string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	logged := false
	for {
		if _, err := os.Stat(unavailableFile); err == nil {
			return "", fmt.Errorf("sticky disk is unavailable (marker: %s)", unavailableFile)
		}
		content, err := os.ReadFile(readyFile)
		if err == nil {
			mountRoot := strings.TrimSpace(string(content))
			if mountRoot == "" {
				mountRoot = os.Getenv("RUNS_ON_STICKYDISK_DIR")
			}
			if mountRoot == "" {
				return "", fmt.Errorf("sticky disk ready file %s is empty and RUNS_ON_STICKYDISK_DIR is not set", readyFile)
			}
			return mountRoot, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("sticky disk was not ready after %s (ready file: %s)", timeout, readyFile)
		}
		if !logged {
			action.Infof("Waiting up to %s for sticky disk to be ready...", timeout)
			logged = true
		}
		time.Sleep(readyPollInterval)
	}
}

// PostJob runs the post-step hooks of the requested cache modes before the
// sticky disk is unmounted and snapshotted by the runner's job-completed hook.
func PostJob(action *githubactions.Action, cacheEntries []string) error {
	if !supportedOS() {
		return nil
	}
	requests, err := ParseCacheRequests(cacheEntries)
	if err != nil {
		return err
	}
	var fatalErr error
	seenModes := map[string]bool{}
	for _, request := range requests {
		if request.Custom {
			continue
		}
		mode := request.Mode
		if mode.PostJob == nil || !mode.supportedOn(runtime.GOOS) || seenModes[mode.Name] {
			continue
		}
		seenModes[mode.Name] = true
		if err := mode.PostJob(action); err != nil {
			if mode.PostJobFailureFatal {
				fatalErr = errors.Join(fatalErr, fmt.Errorf("post-job hook for %s cache failed: %w", mode.Name, err))
			} else {
				action.Warningf("Post-job hook for %s cache failed: %v", mode.Name, err)
			}
		}
	}
	return fatalErr
}

// DisplayUsage prints sticky disk timings and per-mount disk usage. Intended
// for the post-execution phase, while the volume is still mounted.
func DisplayUsage(action *githubactions.Action) {
	if timingsFile := os.Getenv("RUNS_ON_STICKYDISK_TIMINGS_FILE"); timingsFile != "" {
		if content, err := os.ReadFile(timingsFile); err == nil {
			action.Infof("Sticky disk timings: %s", strings.TrimSpace(string(content)))
		}
	}
	mountRoot := os.Getenv("RUNS_ON_STICKYDISK_DIR")
	if mountRoot == "" {
		return
	}
	if _, err := os.Stat(mountRoot); err != nil {
		return
	}
	warnOnPressure(action, mountRoot)
}
