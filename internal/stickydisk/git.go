package stickydisk

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/runs-on/action/internal/gitproxy"
	"github.com/sethvargo/go-githubactions"
)

const (
	gitProxyStateFile = "/tmp/runs-on/git-proxy/state.json"
	gitProxyLogFile   = "/tmp/runs-on-git-proxy.log"
	gitProxyStartWait = 10 * time.Second
	gitProxyStopWait  = 10 * time.Second
)

// gitMirrorDir is where repo mirrors live on the sticky disk, so they are
// snapshotted with the rest of the volume.
func gitMirrorDir(mountRoot string) string {
	return filepath.Join(mountRoot, "git", "mirrors")
}

// setupGit accelerates git fetches (most notably an unmodified
// actions/checkout step running AFTER this action) by starting a local git
// proxy backed by mirrors on the sticky disk, and rewriting github.com fetch
// URLs to it via global git config. Pushes are pinned to upstream, and
// anything the proxy cannot serve (LFS, receive-pack) is forwarded to
// github.com, so nothing breaks when the mirror is cold or unavailable.
func setupGit(action *githubactions.Action, mountRoot string) (hit bool, err error) {
	// A cancelled job can leave rewrites behind. Remove them before making any
	// current-job decision, including a GHES skip.
	if err := restoreGitProxyRewrites(); err != nil {
		return false, fmt.Errorf("restore stale git proxy rewrites: %w", err)
	}

	if serverURL := os.Getenv("GITHUB_SERVER_URL"); serverURL != "" && serverURL != "https://github.com" {
		action.Infof("git cache mode only supports github.com (GITHUB_SERVER_URL=%s), skipping.", serverURL)
		return false, nil
	}

	mirrorDir := gitMirrorDir(mountRoot)
	if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
		hit = gitMirrorCacheHit(mirrorDir, repo)
	}

	state, err := ensureGitProxy(action, mirrorDir)
	if err != nil {
		return hit, err
	}

	// Only rewrite URLs once the proxy is confirmed healthy: a failed setup
	// must leave git completely untouched.
	if err := configureGitProxyRewrites(action, state.Port); err != nil {
		return hit, err
	}

	action.Infof("Git mirror proxy ready on 127.0.0.1:%d (mirrors: %s). github.com fetches are served from the sticky disk; run this action BEFORE actions/checkout to accelerate it.", state.Port, mirrorDir)
	return hit, nil
}

func gitMirrorCacheHit(mirrorDir, repo string) bool {
	// GitHub repository identifiers are case-insensitive, and the proxy uses
	// lowercase local mirror keys so alternate URL casing shares one cache.
	repo = strings.ToLower(repo)
	return gitproxy.IsUsableRepo(context.Background(), filepath.Join(mirrorDir, "github.com", repo+".git"))
}

// ensureGitProxy starts the proxy (re-executing this binary with the hidden
// --git-proxy-serve flag) unless a previous instance from this job is still
// healthy, and waits for it to publish its state file.
func ensureGitProxy(action *githubactions.Action, mirrorDir string) (gitproxy.State, error) {
	owner := gitProxyOwner()
	if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil {
		if gitProxyHealthy(state.Port) && gitProxyStateMatches(state, mirrorDir, owner) {
			action.Infof("Reusing running git proxy on port %d", state.Port)
			return state, nil
		}
		// Any recorded proxy that cannot be safely reused may still have live
		// maintenance children writing to a detached sticky volume. Terminate
		// it before removing the only state file that identifies its PID.
		action.Warningf("Stopping stale or unhealthy git proxy on port %d before starting a replacement.", state.Port)
		if err := terminateGitProxy(action, state.PID); err != nil {
			return gitproxy.State{}, fmt.Errorf("stop stale git proxy: %w", err)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return gitproxy.State{}, fmt.Errorf("cannot locate action binary: %w", err)
	}

	logFile, err := openGitProxyLog(gitProxyLogFile)
	if err != nil {
		return gitproxy.State{}, fmt.Errorf("cannot open proxy log file: %w", err)
	}
	defer logFile.Close()

	os.Remove(gitProxyStateFile)
	cmd := exec.Command(self, "--git-proxy-serve")
	cmd.Env = append(os.Environ(),
		gitproxy.EnvMirrorDir+"="+mirrorDir,
		gitproxy.EnvStateFile+"="+gitProxyStateFile,
		gitproxy.EnvOwner+"="+owner,
	)
	if os.Getenv("RUNNER_DEBUG") == "1" {
		cmd.Env = append(cmd.Env, gitproxy.EnvDebug+"=1")
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return gitproxy.State{}, fmt.Errorf("failed to start git proxy: %w", err)
	}
	// The proxy must outlive this step: it serves fetches for the whole job
	// and is stopped by the post step.
	if err := cmd.Process.Release(); err != nil {
		return gitproxy.State{}, fmt.Errorf("failed to detach git proxy: %w", err)
	}

	deadline := time.Now().Add(gitProxyStartWait)
	for {
		if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil && gitProxyStateMatches(state, mirrorDir, owner) && gitProxyHealthy(state.Port) {
			return state, nil
		}
		if time.Now().After(deadline) {
			logProxyTail(action)
			return gitproxy.State{}, fmt.Errorf("git proxy did not become ready within %s", gitProxyStartWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func gitProxyOwner() string {
	// The runner generates RUNNER_TRACKING_ID per job execution, while
	// GITHUB_JOB is shared by every matrix expansion. Runner names are the
	// best available fallback on older runner versions.
	executionID := os.Getenv("RUNNER_TRACKING_ID")
	if executionID == "" {
		executionID = os.Getenv("RUNS_ON_RUNNER_NAME")
	}
	if executionID == "" {
		executionID = os.Getenv("RUNNER_NAME")
	}
	return strings.Join([]string{
		os.Getenv("GITHUB_RUN_ID"),
		os.Getenv("GITHUB_RUN_ATTEMPT"),
		os.Getenv("GITHUB_JOB"),
		executionID,
	}, "/")
}

func gitProxyStateMatches(state gitproxy.State, mirrorDir, owner string) bool {
	return state.MirrorDir != "" &&
		filepath.Clean(state.MirrorDir) == filepath.Clean(mirrorDir) &&
		state.Owner == owner
}

func gitProxyHealthy(port int) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// configureGitProxyRewrites installs the global git config that routes
// github.com fetches through the proxy:
//   - insteadOf rewrites https://github.com/ fetch URLs to the proxy
//     (SSH remotes are deliberately left alone: they use their own keys)
//   - pushInsteadOf pins pushes to upstream (it takes precedence over
//     insteadOf for pushes), so `git push` never touches the proxy and keeps
//     using actions/checkout's persisted credential
//   - extraheader authenticates requests to the proxy, which forwards the
//     header to github.com when syncing mirrors. Needed because after the URL
//     rewrite, checkout's own http.https://github.com/.extraheader no longer
//     matches the effective URL.
func configureGitProxyRewrites(action *githubactions.Action, port int) error {
	return configureGitProxyRewritesWith(action, port, gitConfigSet, gitConfigAdd, removeGitProxyRewrites)
}

func configureGitProxyRewritesWith(action *githubactions.Action, port int, set, add func(string, string) error, rollback func() error) (err error) {
	proxyBase := fmt.Sprintf("http://127.0.0.1:%d/", port)
	applied := false
	defer func() {
		if err != nil && applied {
			if rollbackErr := rollback(); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("roll back partial git proxy rewrites: %w", rollbackErr))
			}
		}
	}()

	if err := set(fmt.Sprintf("url.%sgithub.com/.insteadOf", proxyBase), "https://github.com/"); err != nil {
		return err
	}
	applied = true
	// pushInsteadOf is a shared user key, so append our identity mapping
	// without replacing runner- or workflow-provided transport mappings.
	if err := add("url.https://github.com/.pushInsteadOf", "https://github.com/"); err != nil {
		return err
	}
	if token := action.GetInput("token"); token != "" {
		b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		action.AddMask(b64)
		if err := set(fmt.Sprintf("http.%s.extraheader", proxyBase), "AUTHORIZATION: basic "+b64); err != nil {
			return err
		}
	} else {
		action.Warningf("No token input available: private repo mirroring will not work.")
	}
	return nil
}

// gitConfigSet writes a global git config entry. Errors deliberately omit
// the value, which may embed credentials.
func gitConfigSet(key, value string) error {
	if output, err := exec.Command("git", "config", "--global", "--replace-all", key, value).CombinedOutput(); err != nil {
		return fmt.Errorf("git config %s failed: %w\noutput: %s", key, err, output)
	}
	return nil
}

func removeGitProxyRewrites() error {
	const pattern = `^(url\.http://127\.0\.0\.1:[0-9]+/github\.com/\.insteadof|http\.http://127\.0\.0\.1:[0-9]+/\.extraheader|url\.https://github\.com/\.pushinsteadof)$`
	out, err := exec.Command("git", "config", "--global", "--name-only", "--get-regexp", pattern).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("find git proxy rewrites: %w", err)
	}

	var removeErr error
	seen := map[string]bool{}
	for _, key := range strings.Fields(string(out)) {
		if seen[key] {
			continue
		}
		seen[key] = true
		output, err := exec.Command("git", "config", "--global", "--unset-all", key).CombinedOutput()
		if err != nil {
			removeErr = errors.Join(removeErr, fmt.Errorf("git config --unset-all %s failed: %w\noutput: %s", key, err, output))
		}
	}
	return removeErr
}

func gitConfigAdd(key, value string) error {
	if output, err := exec.Command("git", "config", "--global", "--add", key, value).CombinedOutput(); err != nil {
		return fmt.Errorf("git config --add %s failed: %w\noutput: %s", key, err, output)
	}
	return nil
}

func restoreGitProxyRewrites() error {
	return removeGitProxyRewrites()
}

// RestoreStaleGitProxyRewrites repairs global Git configuration left by an
// interrupted prior job. It must run for every action invocation, even when
// the current job does not request the Git cache mode.
func RestoreStaleGitProxyRewrites() error {
	if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil &&
		!shouldRestoreGitProxyRewrites(state, gitProxyOwner(), gitProxyHealthy(state.Port)) {
		return nil
	}
	return restoreGitProxyRewrites()
}

func shouldRestoreGitProxyRewrites(state gitproxy.State, owner string, healthy bool) bool {
	return state.Owner != owner || !healthy
}

// stopGit removes the URL rewrites then stops the proxy, in that order: git
// config must never point at a dead proxy. Runs in the post step, before the
// runner's job-completed hook unmounts and snapshots the sticky disk.
func stopGit(action *githubactions.Action) error {
	err := restoreRewritesThenStop(restoreGitProxyRewrites, func() error {
		if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil {
			return terminateGitProxy(action, state.PID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("git cache mode cleanup: %w", err)
	}

	if os.Getenv("RUNNER_DEBUG") == "1" {
		logProxyTail(action)
	}
	if err := removeFileIfExists(gitProxyLogFile); err != nil {
		action.Warningf("Failed to remove git proxy log: %v", err)
	}
	return nil
}

// restoreRewritesThenStop preserves the proxy whenever Git configuration could
// not be restored. A live localhost endpoint is safer than leaving insteadOf
// pointed at a dead port on a reused runner.
func restoreRewritesThenStop(restore, stop func() error) error {
	if err := restore(); err != nil {
		return fmt.Errorf("restore git proxy rewrites; proxy left running: %w", err)
	}
	return stop()
}

// terminateGitProxy asks the proxy to shut down gracefully and escalates to
// SIGKILL if it lingers.
func terminateGitProxy(action *githubactions.Action, pid int) error {
	if !processIsGitProxy(pid) {
		action.Warningf("Recorded git proxy pid %d no longer identifies a --git-proxy-serve process; leaving it untouched.", pid)
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := signalTerm(proc); err != nil {
		// Already gone.
		return nil
	}
	action.Infof("Stopping git proxy (pid %d)...", pid)
	deadline := time.Now().Add(gitProxyStopWait)
	for time.Now().Before(deadline) {
		if !processAlive(proc) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	action.Warningf("git proxy did not stop within %s, killing it", gitProxyStopWait)
	return proc.Kill()
}

func commandLineHasGitProxyServe(command string) bool {
	for _, arg := range strings.Fields(strings.ReplaceAll(command, "\x00", " ")) {
		if arg == "--git-proxy-serve" {
			return true
		}
	}
	return false
}

func openGitProxyLog(path string) (*os.File, error) {
	// Self-hosted runners retain /tmp across jobs. Truncate when ownership
	// transfers to a new proxy so stale logs cannot grow without bound.
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}

func readFileTail(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := info.Size() - limit
	prefix := ""
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
		prefix = "..."
	}
	log, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", err
	}
	return prefix + string(log), nil
}

func removeFileIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func logProxyTail(action *githubactions.Action) {
	if log, err := readFileTail(gitProxyLogFile, 4000); err == nil && log != "" {
		action.Infof("git proxy log:\n%s", log)
	}
}
