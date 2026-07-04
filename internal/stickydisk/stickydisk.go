package stickydisk

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

const (
	defaultReadyFile   = "/runs-on/stickydisk.ready"
	defaultWaitTimeout = 5 * time.Minute
	readyPollInterval  = 500 * time.Millisecond
)

// Options configures the sticky disk cache setup.
type Options struct {
	// Modes is the list of cache modes from the `cache` input.
	Modes []string
	// Paths is the list of arbitrary paths from the `path` input.
	Paths []string
	// WaitTimeout bounds how long to wait for the sticky disk to be ready.
	WaitTimeout time.Duration
	// FailOnMissing makes the action fail when no sticky disk is available.
	FailOnMissing bool
}

type mountResult struct {
	Target string
	Hit    bool
	Err    error
}

// Configure bind-mounts the requested cache directories onto the job's sticky
// disk. It requires a `snap=<size>[:<name>]` label on the job. Mount failures
// are reported as warnings; only a missing/unready sticky disk combined with
// FailOnMissing returns an error.
func Configure(action *githubactions.Action, opts Options) error {
	if runtime.GOOS != "linux" {
		action.Warningf("Sticky disk cache is only supported on Linux runners, skipping.")
		action.SetOutput("cache-hit", "false")
		return nil
	}

	readyFile := os.Getenv("RUNS_ON_STICKYDISK_READY_FILE")
	if readyFile == "" {
		readyFile = defaultReadyFile
	}

	if os.Getenv("RUNS_ON_STICKYDISK_DIR") == "" {
		if _, err := os.Stat(readyFile); os.IsNotExist(err) {
			return missing(action, opts, "No sticky disk detected. Add a snap=<size>[:<name>] label to your runs-on labels to enable the cache.")
		}
	}

	timeout := opts.WaitTimeout
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	mountRoot, err := waitForReady(action, readyFile, timeout)
	if err != nil {
		return missing(action, opts, err.Error())
	}
	action.Infof("Sticky disk ready at %s (name: %s)", mountRoot, os.Getenv("RUNS_ON_STICKYDISK_NAME"))

	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/runner"
	}
	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		workspace, _ = os.Getwd()
	}

	modes, unknown := ResolveModes(opts.Modes)
	for _, name := range unknown {
		action.Warningf("Unknown cache mode '%s'. Valid modes: %s", name, strings.Join(ValidModes(), ", "))
	}

	// Resolve all targets, deduplicating by absolute path.
	type mountSpec struct {
		target string
		root   bool
	}
	var specs []mountSpec
	var posts []func(action *githubactions.Action) error
	var results []mountResult
	seen := map[string]bool{}
	addTarget := func(path string, root bool) {
		resolved := resolveTarget(path, home, workspace)
		if seen[resolved] {
			return
		}
		seen[resolved] = true
		specs = append(specs, mountSpec{target: resolved, root: root})
	}
	for _, mode := range modes {
		// Modes with a Setup function own their whole setup (no bind mounts).
		if mode.Setup != nil {
			hit, err := mode.Setup(action, mountRoot)
			if err != nil {
				action.Warningf("Failed to set up %s cache: %v", mode.Name, err)
			}
			results = append(results, mountResult{Target: mode.Name, Hit: hit && err == nil, Err: err})
			continue
		}
		for _, path := range mode.Paths {
			addTarget(path, mode.Root)
		}
		if mode.Post != nil {
			posts = append(posts, mode.Post)
		}
	}
	for _, path := range opts.Paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		addTarget(strings.TrimSpace(path), false)
	}

	if len(specs) == 0 && len(results) == 0 {
		action.Warningf("No cache paths to set up.")
		action.SetOutput("cache-hit", "false")
		return nil
	}

	for _, spec := range specs {
		hit, err := bindMount(action, mountRoot, spec.target, spec.root)
		if err != nil {
			action.Warningf("Failed to set up cache for %s: %v", spec.target, err)
		}
		results = append(results, mountResult{Target: spec.target, Hit: hit && err == nil, Err: err})
	}

	for _, post := range posts {
		if err := post(action); err != nil {
			action.Warningf("Cache post-setup step failed: %v", err)
		}
	}

	allHit := true
	action.Infof("Sticky disk cache summary:")
	for _, res := range results {
		status := "restored"
		if res.Err != nil {
			status = "error"
		} else if !res.Hit {
			status = "empty"
		}
		if !res.Hit {
			allHit = false
		}
		action.Infof("  %-10s %s", status, res.Target)
	}
	action.SetOutput("cache-hit", strconv.FormatBool(allHit))
	return nil
}

// missing handles the no-sticky-disk case: a warning by default, an error
// when FailOnMissing is set.
func missing(action *githubactions.Action, opts Options, msg string) error {
	action.SetOutput("cache-hit", "false")
	if opts.FailOnMissing {
		return fmt.Errorf("%s", msg)
	}
	action.Warningf("%s Continuing without cache.", msg)
	return nil
}

// waitForReady polls the ready file until it exists or the timeout elapses,
// returning the sticky disk mount root.
func waitForReady(action *githubactions.Action, readyFile string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	logged := false
	for {
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

// PostJob runs the post-step hooks of the requested cache modes (e.g. stop
// buildkitd), before the sticky disk is unmounted and snapshotted by the
// runner's job-completed hook.
func PostJob(action *githubactions.Action, modeNames []string) {
	if runtime.GOOS != "linux" {
		return
	}
	modes, _ := ResolveModes(modeNames)
	for _, mode := range modes {
		if mode.PostJob == nil {
			continue
		}
		if err := mode.PostJob(action); err != nil {
			action.Warningf("Post-job hook for %s cache failed: %v", mode.Name, err)
		}
	}
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
	mountsDir := filepath.Join(mountRoot, "mounts")
	if _, err := os.Stat(mountsDir); err != nil {
		return
	}
	out, err := exec.Command("sudo", "du", "-sh", mountsDir).CombinedOutput()
	if err == nil {
		action.Infof("Sticky disk cache usage: %s", strings.TrimSpace(string(out)))
	}
}
