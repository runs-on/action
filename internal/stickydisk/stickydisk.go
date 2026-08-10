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
	defaultWaitTimeout = 15 * time.Minute
	readyPollInterval  = 500 * time.Millisecond
)

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
	// Publish a deterministic value even when parsing, ordering, setup, or
	// overlap validation fails and the caller uses continue-on-error.
	action.SetOutput("cache-hit", "false")

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
	if err := validateCacheOrdering(requests, runtime.GOOS, home, workspace); err != nil {
		return err
	}

	contract, err := readStickyDiskContract()
	if err != nil {
		return missing(action, err.Error())
	}

	timeout := opts.StickyWaitTimeout
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	if err := waitForReady(action, contract.ReadyFile, contract.UnavailableFile, timeout); err != nil {
		return missing(action, err.Error())
	}
	// The ready marker is an existence-only signal: the mount root always
	// comes from the agent-provided environment, never from file content that
	// could be stale or point at an unrelated mount.
	mountRoot := contract.MountRoot
	if err := validateStickyMount(mountRoot); err != nil {
		return missing(action, err.Error())
	}
	action.Infof("Sticky disk ready at %s (name: %s)", mountRoot, os.Getenv(stickyDiskNameEnv))

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
	state, err := readJobCacheState()
	if err != nil {
		return fmt.Errorf("read cache state from earlier action invocations: %w", err)
	}
	addTarget := func(path string, root bool) (string, error) {
		resolved := resolveTarget(path, home, workspace)
		// Every workspace-relative target gets ancestry validation, whether it
		// came from a custom record or a built-in mode (e.g. ruby's
		// vendor/bundle): the checkout tree below the workspace is less
		// trusted than the workflow inputs.
		if err := validateWorkspaceAncestry(workspace, resolved); err != nil {
			return "", err
		}
		canonical := canonicalPath(resolved)
		if seen[canonical] {
			return resolved, nil
		}
		seen[canonical] = true
		specs = append(specs, mountSpec{target: resolved, root: root})
		return resolved, nil
	}
	for _, request := range requests {
		if request.Custom {
			for _, path := range request.Paths {
				if _, err := addTarget(path, false); err != nil {
					return err
				}
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
			stateKey := modeStateKey(mode.Name)
			// A repeated invocation (e.g. the token-change proxy restart) must
			// preserve the original cold/warm result: by now checkout has
			// populated the mirror, which is not a restored snapshot.
			if originalHit, found := state.Modes[stateKey]; found {
				hit = originalHit
			}
			if err != nil {
				if mode.SetupFailureFatal {
					return fmt.Errorf("failed to set up %s cache: %w", mode.Name, err)
				}
				action.Warningf("Failed to set up %s cache: %v", mode.Name, err)
			} else {
				state.Modes[stateKey] = hit
			}
			results = append(results, mountResult{Target: mode.Name, Hit: hit && err == nil, Err: err})
			continue
		}
		paths, err := mode.pathsFor(runtime.GOOS)
		if err != nil {
			return err
		}
		var modeTargets []string
		for _, path := range paths {
			resolved, err := addTarget(path, mode.Root)
			if err != nil {
				return err
			}
			modeTargets = append(modeTargets, resolved)
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
	if len(targets) > 0 {
		mountedTargets, err := activeCacheTargets(mountRoot)
		if err != nil {
			return fmt.Errorf("inspect cache targets mounted by earlier action invocations: %w", err)
		}
		if err := validateNoOverlappingTargetsWithMounted(targets, mountedTargets, mountRoot); err != nil {
			return err
		}
	}
	if len(specs) == 0 && len(results) == 0 {
		action.Warningf("No cache paths to set up.")
		action.SetOutput("cache-hit", "false")
		return nil
	}

	mountErrors := make(map[string]error, len(specs))
	for _, spec := range specs {
		hit, err := cacheMount(action, mountRoot, spec.target, spec.root)
		key := canonicalPath(spec.target)
		if originalHit, found := state.Mounts[key]; found {
			// A repeated invocation in the same job must preserve the original
			// cold/warm result instead of treating files written by an earlier
			// step as a restored snapshot.
			hit = originalHit
		}
		if err != nil {
			action.Warningf("Failed to set up cache for %s: %v", spec.target, err)
		} else {
			state.Mounts[key] = hit
		}
		results = append(results, mountResult{Target: spec.target, Hit: hit && err == nil, Err: err})
		mountErrors[spec.target] = err
	}
	if err := writeJobCacheState(action, state); err != nil {
		return fmt.Errorf("write cache state for later action invocations: %w", err)
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
	return validateNoOverlappingTargetsWithMounted(targets, nil, mountRoot)
}

func validateNoOverlappingTargetsWithMounted(targets, mountedTargets []string, mountRoot string) error {
	canonical := make([]string, len(targets))
	for i, target := range targets {
		canonical[i] = canonicalPath(target)
	}
	canonicalMountRoot := canonicalPath(mountRoot)
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
	for i, target := range canonical {
		for j, mountedTarget := range mountedTargets {
			mountedTarget = canonicalPath(mountedTarget)
			if target == mountedTarget {
				continue
			}
			if pathContains(target, mountedTarget) || pathContains(mountedTarget, target) {
				return fmt.Errorf("sticky cache path %s overlaps %s mounted by an earlier runs-on/action invocation", targets[i], mountedTargets[j])
			}
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(canonicalPath(parent), canonicalPath(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
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

func validateCacheOrdering(requests []CacheRequest, goos, home, workspace string) error {
	for _, request := range requests {
		if request.Custom || request.Mode.Setup != nil {
			continue
		}
		if _, err := request.Mode.pathsFor(goos); err != nil {
			return err
		}
	}
	if goos == "windows" {
		return nil
	}

	hasGit := false
	gitModes := 0
	for _, request := range requests {
		if !request.Custom {
			switch request.Mode.Name {
			case "git", "git-full":
				hasGit = true
				gitModes++
			}
		}
	}
	if gitModes > 1 {
		return fmt.Errorf("git and git-full cache modes are mutually exclusive")
	}
	if !hasGit {
		return nil
	}

	var workspaceModes []string
	for _, request := range requests {
		if !request.Custom && (request.Mode.Name == "git" || request.Mode.Name == "git-full") {
			continue
		}
		paths := request.Paths
		name := "custom"
		if !request.Custom {
			name = request.Mode.Name
			modePaths, err := request.Mode.pathsFor(goos)
			if err != nil {
				return err
			}
			paths = modePaths
		}
		for _, path := range paths {
			if isWorkspaceCachePath(path, home, workspace) {
				workspaceModes = append(workspaceModes, name)
			}
		}
	}

	if len(workspaceModes) > 0 {
		return fmt.Errorf("git cache mode cannot be combined with workspace-relative cache modes (%s) in one invocation; run git before checkout, then run workspace-relative caches in a second runs-on/action step after checkout", strings.Join(workspaceModes, ", "))
	}
	return nil
}

// isWorkspaceCachePath resolves the path exactly like mounting will (~, home,
// relative) before testing workspace containment: a home-relative path such
// as ~/work/org/repo/vendor can land inside the checkout when the workspace
// lives under the home directory, and must not bypass the git-ordering guard.
// Containment is checked in both directions — a target that is an ancestor of
// the workspace (~/work) would equally mount restored state over the checkout
// location before actions/checkout runs.
func isWorkspaceCachePath(path, home, workspace string) bool {
	if workspace == "" {
		return !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/")
	}
	resolved := filepath.Clean(resolveTarget(path, home, workspace))
	workspace = filepath.Clean(workspace)
	return pathContains(workspace, resolved) || pathContains(resolved, workspace)
}

// missing handles the no-sticky-disk case. Requesting sticky_cache is an
// explicit persistence contract, so absence or readiness timeout is fatal.
func missing(action *githubactions.Action, msg string) error {
	action.SetOutput("cache-hit", "false")
	return errors.New(msg)
}

// waitForReady polls until the agent publishes either the mounted-ready marker
// or the terminal-unavailable marker, bounded by timeout. Both markers are
// existence-only signals; their content is deliberately ignored.
func waitForReady(action *githubactions.Action, readyFile string, unavailableFile string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	logged := false
	for {
		if _, err := os.Stat(unavailableFile); err == nil {
			return fmt.Errorf("sticky disk is unavailable (marker: %s)", unavailableFile)
		}
		if _, err := os.Stat(readyFile); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sticky disk was not ready after %s (ready file: %s)", timeout, readyFile)
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
	if timingsFile := os.Getenv(stickyDiskTimingsFileEnv); timingsFile != "" {
		if content, err := os.ReadFile(timingsFile); err == nil {
			action.Infof("Sticky disk timings: %s", strings.TrimSpace(string(content)))
		}
	}
	mountRoot := os.Getenv(stickyDiskDirEnv)
	if mountRoot == "" {
		return
	}
	if _, err := os.Stat(mountRoot); err != nil {
		return
	}
	warnOnPressure(action, mountRoot)
}
