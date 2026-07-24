package stickydisk

import (
	"encoding/base64"
	"errors"
	"fmt"
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
	if serverURL := os.Getenv("GITHUB_SERVER_URL"); serverURL != "" && serverURL != "https://github.com" {
		action.Infof("git cache mode only supports github.com (GITHUB_SERVER_URL=%s), skipping.", serverURL)
		return false, nil
	}

	mirrorDir := gitMirrorDir(mountRoot)
	if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
		hit = dirNonEmpty(filepath.Join(mirrorDir, "github.com", repo+".git"))
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

// ensureGitProxy starts the proxy (re-executing this binary with the hidden
// --git-proxy-serve flag) unless a previous instance from this job is still
// healthy, and waits for it to publish its state file.
func ensureGitProxy(action *githubactions.Action, mirrorDir string) (gitproxy.State, error) {
	if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil && gitProxyHealthy(state.Port) {
		action.Infof("Reusing running git proxy on port %d", state.Port)
		return state, nil
	}

	self, err := os.Executable()
	if err != nil {
		return gitproxy.State{}, fmt.Errorf("cannot locate action binary: %w", err)
	}

	logFile, err := os.OpenFile(gitProxyLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return gitproxy.State{}, fmt.Errorf("cannot open proxy log file: %w", err)
	}
	defer logFile.Close()

	os.Remove(gitProxyStateFile)
	cmd := exec.Command(self, "--git-proxy-serve")
	cmd.Env = append(os.Environ(),
		gitproxy.EnvMirrorDir+"="+mirrorDir,
		gitproxy.EnvStateFile+"="+gitProxyStateFile,
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
		if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil && gitProxyHealthy(state.Port) {
			return state, nil
		}
		if time.Now().After(deadline) {
			logProxyTail(action)
			return gitproxy.State{}, fmt.Errorf("git proxy did not become ready within %s", gitProxyStartWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
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
	return configureGitProxyRewritesWith(action, port, gitConfigSet, removeGitProxyRewrites)
}

func configureGitProxyRewritesWith(action *githubactions.Action, port int, set func(string, string) error, rollback func() error) (err error) {
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
	if err := set("url.https://github.com/.pushInsteadOf", "https://github.com/"); err != nil {
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

// stopGit removes the URL rewrites then stops the proxy, in that order: git
// config must never point at a dead proxy. Runs in the post step, before the
// runner's job-completed hook unmounts and snapshots the sticky disk.
func stopGit(action *githubactions.Action) error {
	var errs []string

	if err := removeGitProxyRewrites(); err != nil {
		errs = append(errs, err.Error())
	}

	if state, err := gitproxy.ReadStateFile(gitProxyStateFile); err == nil {
		if err := terminateGitProxy(action, state.PID); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if os.Getenv("RUNNER_DEBUG") == "1" {
		logProxyTail(action)
	}
	if len(errs) > 0 {
		return fmt.Errorf("git cache mode cleanup: %s", strings.Join(errs, "; "))
	}
	return nil
}

// removeGitProxyRewrites clears every config entry pointing at the proxy,
// discovered by scanning for the loopback URL rather than trusting the state
// file, plus the push pin.
func removeGitProxyRewrites() error {
	out, _ := exec.Command("git", "config", "--global", "--name-only", "--get-regexp", `^(url|http)\.http://127\.0\.0\.1:`).Output()
	for _, name := range strings.Fields(string(out)) {
		if output, err := exec.Command("git", "config", "--global", "--unset-all", name).CombinedOutput(); err != nil {
			return fmt.Errorf("git config --unset-all %s failed: %w\noutput: %s", name, err, output)
		}
	}
	// Exit code 5 means the key was not set; anything else is a real error.
	if output, err := exec.Command("git", "config", "--global", "--unset-all", "url.https://github.com/.pushInsteadOf").CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 5 {
			return fmt.Errorf("git config --unset-all pushInsteadOf failed: %w\noutput: %s", err, output)
		}
	}
	return nil
}

// terminateGitProxy asks the proxy to shut down gracefully and escalates to
// SIGKILL if it lingers.
func terminateGitProxy(action *githubactions.Action, pid int) error {
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

func logProxyTail(action *githubactions.Action) {
	if log, err := os.ReadFile(gitProxyLogFile); err == nil && len(log) > 0 {
		action.Infof("git proxy log:\n%s", tailString(string(log), 4000))
	}
}
