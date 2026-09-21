package stickydisk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/runs-on/action/internal/gitproxy"
	"github.com/sethvargo/go-githubactions"
)

const (
	gitProxyStartWait = 10 * time.Second
	gitProxyStopWait  = 12 * time.Second
	gitProxyKillWait  = 2 * time.Second

	gitProxyPIDStateKey       = "runs_on_git_proxy_pid"
	gitProxyPIDStateEnv       = "STATE_" + gitProxyPIDStateKey
	gitProxyMirrorDirStateKey = "runs_on_git_proxy_mirror_dir"
	gitProxyMirrorDirStateEnv = "STATE_" + gitProxyMirrorDirStateKey
)

func gitProxyStateFile() string {
	return runnerTempPath("runs-on", "git-proxy", "state.json")
}

func gitProxyLogFile() string {
	return runnerTempPath("runs-on-git-proxy.log")
}

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
	return setupGitMode(action, mountRoot, false)
}

func setupGitFull(action *githubactions.Action, mountRoot string) (hit bool, err error) {
	return setupGitMode(action, mountRoot, true)
}

func setupGitMode(action *githubactions.Action, mountRoot string, allRefs bool) (hit bool, err error) {
	// A cancelled job can leave rewrites behind. Remove them before making any
	// current-job decision, including a GHES skip.
	if err := restoreGitProxyRewrites(); err != nil {
		return false, fmt.Errorf("restore stale git proxy rewrites: %w", err)
	}

	if serverURL := os.Getenv("GITHUB_SERVER_URL"); serverURL != "" && serverURL != "https://github.com" {
		action.Infof("git cache mode only supports github.com (GITHUB_SERVER_URL=%s), skipping.", serverURL)
		return false, nil
	}
	policy, err := currentGitProxyPolicy(allRefs)
	if err != nil {
		return false, err
	}

	mirrorDir := gitMirrorDir(mountRoot)
	if err := ensureRealDirectoryPath(mountRoot, mirrorDir); err != nil {
		return false, fmt.Errorf("validate Git mirror directory: %w", err)
	}
	if repo := os.Getenv("GITHUB_REPOSITORY"); repo != "" {
		hit = gitMirrorCacheHit(mirrorDir, repo, !allRefs)
	}

	state, clientToken, err := ensureGitProxy(action, mirrorDir, policy)
	if err != nil {
		return hit, err
	}

	// Only rewrite URLs once the proxy is confirmed healthy: a failed setup
	// must leave git completely untouched.
	if err := configureGitProxyRewrites(action, state.Port, clientToken); err != nil {
		return hit, err
	}

	if allRefs {
		action.Infof("Full Git mirror proxy ready on 127.0.0.1:%d (mirrors: %s). All github.com fetches are served from the sticky disk; run this action BEFORE actions/checkout to accelerate it.", state.Port, mirrorDir)
	} else {
		action.Infof("Git mirror proxy ready on 127.0.0.1:%d (mirrors: %s). The workflow repository history at %s is cached; other github.com fetches go upstream.", state.Port, mirrorDir, policy.ref)
	}
	return hit, nil
}

type gitProxyPolicy struct {
	allRefs    bool
	repository string
	ref        string
}

func currentGitProxyPolicy(allRefs bool) (gitProxyPolicy, error) {
	if allRefs {
		return gitProxyPolicy{allRefs: true}, nil
	}
	repository := strings.ToLower(strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")))
	ref := strings.TrimSpace(os.Getenv("GITHUB_SHA"))
	if repository == "" || ref == "" {
		return gitProxyPolicy{}, fmt.Errorf("git cache mode requires GITHUB_REPOSITORY and GITHUB_SHA")
	}
	if len(ref) != 40 || !isHex(ref) {
		return gitProxyPolicy{}, fmt.Errorf("GITHUB_SHA must be a 40-character SHA-1 object ID")
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return gitProxyPolicy{}, fmt.Errorf("GITHUB_REPOSITORY must use owner/name")
	}
	return gitProxyPolicy{repository: repository, ref: ref}, nil
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func (p gitProxyPolicy) digest() string {
	value := fmt.Sprintf("all=%t;repository=%s;ref=%s", p.allRefs, p.repository, p.ref)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func gitMirrorCacheHit(mirrorDir, repo string, scoped bool) bool {
	// GitHub repository identifiers are case-insensitive, and the proxy uses
	// lowercase local mirror keys so alternate URL casing shares one cache.
	repo = strings.ToLower(repo)
	repoPath := filepath.Join(mirrorDir, "github.com", repo+".git")
	if scoped {
		return gitproxy.IsScopedUsableRepo(context.Background(), mirrorDir, repoPath)
	}
	return gitproxy.IsUsableRepo(context.Background(), mirrorDir, repoPath)
}

// ensureGitProxy starts the proxy (re-executing this binary with the hidden
// --git-proxy-serve flag) unless a previous instance from this job is still
// healthy, and waits for it to publish its state file. It returns the opaque
// per-job client token that git clients must present: the real GitHub
// credential stays inside the proxy process and never enters git config.
func ensureGitProxy(action *githubactions.Action, mirrorDir string, policy gitProxyPolicy) (gitproxy.State, string, error) {
	owner := gitProxyOwner()
	clientToken := os.Getenv(gitproxy.EnvClientToken)
	if clientToken == "" {
		var err error
		clientToken, err = newGitProxySecret()
		if err != nil {
			return gitproxy.State{}, "", err
		}
		action.AddMask(clientToken)
		action.SetEnv(gitproxy.EnvClientToken, clientToken)
	}
	healthToken := os.Getenv(gitproxy.EnvHealthToken)
	if healthToken == "" {
		var err error
		healthToken, err = newGitProxySecret()
		if err != nil {
			return gitproxy.State{}, "", err
		}
		action.AddMask(healthToken)
		action.SetEnv(gitproxy.EnvHealthToken, healthToken)
	}
	upstreamToken := action.GetInput("token")
	upstreamDigest := upstreamTokenDigest(upstreamToken)
	sameUpstream := os.Getenv(gitProxyUpstreamDigestEnv) == upstreamDigest
	action.SetEnv(gitProxyUpstreamDigestEnv, upstreamDigest)
	policyDigest := policy.digest()
	samePolicy := os.Getenv(gitProxyPolicyDigestEnv) == policyDigest
	action.SetEnv(gitProxyPolicyDigestEnv, policyDigest)
	if state, err := gitproxy.ReadStateFile(gitProxyStateFile()); err == nil {
		if sameUpstream && samePolicy && gitProxyHealthy(state.Port, healthToken) && gitProxyStateMatches(state, mirrorDir, owner) {
			saveGitProxyPostState(action, state)
			action.Infof("Reusing running git proxy on port %d", state.Port)
			return state, clientToken, nil
		}
		// Any recorded proxy that cannot be safely reused may still have live
		// maintenance children writing to a detached sticky volume (or, when
		// the token input changed, keep syncing with the previous credential).
		// Terminate it before removing the only state file that identifies its
		// PID.
		action.Warningf("Stopping stale, unhealthy, or differently-credentialed git proxy on port %d before starting a replacement.", state.Port)
		if err := terminateGitProxy(action, state.PID); err != nil {
			return gitproxy.State{}, "", fmt.Errorf("stop stale git proxy: %w", err)
		}
	} else if !os.IsNotExist(err) {
		// Without a trustworthy PID, replacing a corrupt/unreadable state file
		// could leave an old proxy or git child writing to the sticky disk.
		return gitproxy.State{}, "", fmt.Errorf("read existing git proxy state: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return gitproxy.State{}, "", fmt.Errorf("cannot locate action binary: %w", err)
	}

	logFile, err := openGitProxyLog(gitProxyLogFile())
	if err != nil {
		return gitproxy.State{}, "", fmt.Errorf("cannot open proxy log file: %w", err)
	}
	defer logFile.Close()

	os.Remove(gitProxyStateFile())
	cmd := exec.Command(self, "--git-proxy-serve")
	cmd.Env = append(proxyChildEnviron(),
		gitproxy.EnvMirrorDir+"="+mirrorDir,
		gitproxy.EnvStateFile+"="+gitProxyStateFile(),
		gitproxy.EnvOwner+"="+owner,
		gitproxy.EnvHealthToken+"="+healthToken,
		gitproxy.EnvClientToken+"="+clientToken,
	)
	if policy.allRefs {
		cmd.Env = append(cmd.Env, gitproxy.EnvAllRefs+"=true")
	} else {
		cmd.Env = append(cmd.Env,
			gitproxy.EnvRepository+"="+policy.repository,
			gitproxy.EnvRef+"="+policy.ref,
		)
	}
	// The upstream credential travels over stdin, never the environment:
	// /proc/<pid>/environ is readable by any same-uid process for the proxy's
	// whole lifetime, while stdin is consumed once at startup.
	cmd.Stdin = strings.NewReader(upstreamToken + "\n")
	if os.Getenv("RUNNER_DEBUG") == "1" {
		cmd.Env = append(cmd.Env, gitproxy.EnvDebug+"=1")
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return gitproxy.State{}, "", fmt.Errorf("failed to start git proxy: %w", err)
	}
	saveGitProxyPostState(action, gitproxy.State{PID: cmd.Process.Pid, MirrorDir: mirrorDir})
	// The proxy must outlive this step: it serves fetches for the whole job
	// and is stopped by the post step.
	if err := cmd.Process.Release(); err != nil {
		return gitproxy.State{}, "", fmt.Errorf("failed to detach git proxy: %w", err)
	}

	deadline := time.Now().Add(gitProxyStartWait)
	for {
		if state, err := gitproxy.ReadStateFile(gitProxyStateFile()); err == nil && gitProxyStateMatches(state, mirrorDir, owner) && gitProxyHealthy(state.Port, healthToken) {
			return state, clientToken, nil
		}
		if time.Now().After(deadline) {
			logProxyTail(action)
			return gitproxy.State{}, "", fmt.Errorf("git proxy did not become ready within %s", gitProxyStartWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// proxyChildEnviron strips credential-bearing variables from the environment
// inherited by the long-lived proxy: /proc/<pid>/environ is readable by any
// same-uid process, and the proxy needs none of the action's inputs or the
// runner's service tokens (its own credential arrives over stdin).
func proxyChildEnviron() []string {
	sensitive := []string{
		"INPUT_",
		"ACTIONS_RUNTIME_TOKEN=",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=",
		"ACTIONS_ID_TOKEN_REQUEST_URL=",
		"GITHUB_TOKEN=",
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		keep := true
		for _, prefix := range sensitive {
			if strings.HasPrefix(kv, prefix) {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	return env
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

// gitProxyUpstreamDigestEnv carries a fingerprint of the upstream credential
// between invocations (via GITHUB_ENV) so a changed token input restarts the
// proxy instead of silently keeping the previous credential active. The
// digest is preimage-resistant and GitHub tokens are high-entropy, so
// exposing it to later steps reveals nothing about the token.
const gitProxyUpstreamDigestEnv = "RUNS_ON_GIT_PROXY_UPSTREAM_DIGEST"
const gitProxyPolicyDigestEnv = "RUNS_ON_GIT_PROXY_POLICY_DIGEST"

func upstreamTokenDigest(token string) string {
	sum := sha256.Sum256([]byte("runs-on git proxy upstream token:" + token))
	return hex.EncodeToString(sum[:])
}

func newGitProxySecret() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate git proxy secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func gitProxyHealthy(port int, token string) bool {
	if token == "" {
		return false
	}
	client := http.Client{Timeout: time.Second}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), nil)
	if err != nil {
		return false
	}
	req.Header.Set(gitproxy.HealthTokenHeader, token)
	resp, err := client.Do(req)
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
//   - extraheader authenticates requests to the proxy with an opaque per-job
//     secret. The real GitHub credential never enters git config: the proxy
//     swaps a matching client credential for the real token on upstream
//     operations, so later checkout-controlled steps cannot read the token
//     back out of global config. Needed because after the URL rewrite,
//     checkout's own http.https://github.com/.extraheader no longer matches
//     the effective URL.
func configureGitProxyRewrites(action *githubactions.Action, port int, clientToken string) error {
	addPush, err := shouldAddPushRewrite()
	if err != nil {
		return err
	}
	return configureGitProxyRewritesWith(action, port, clientToken, addPush, gitConfigAdd, removeGitProxyRewrites)
}

// gitProxyAddedPushRewriteEnv records (via GITHUB_ENV) whether this action
// added the identity pushInsteadOf mapping. Cleanup removes the mapping only
// when the action owns it: the runner or workflow may maintain an identical
// value to pin pushes against its own insteadOf transport rewrites, and
// --unset-all cannot distinguish duplicate values.
const gitProxyAddedPushRewriteEnv = "RUNS_ON_GIT_PROXY_ADDED_PUSH_REWRITE"

// shouldAddPushRewrite reports whether the identity mapping needs adding,
// based purely on current presence: absent means add (setupGit removes an
// action-owned entry at the start of every invocation, so a repeated
// invocation must re-add it); present means leave it alone, whether the
// action or the user put it there. The ownership flag is consumed only by
// cleanup.
func shouldAddPushRewrite() (bool, error) {
	out, err := exec.Command("git", "config", "--global", "--get-all", "url.https://github.com/.pushInsteadOf").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, fmt.Errorf("read existing pushInsteadOf values: %w", err)
	}
	for _, value := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if value == "https://github.com/" {
			return false, nil
		}
	}
	return true, nil
}

func configureGitProxyRewritesWith(action *githubactions.Action, port int, clientToken string, addPush bool, add func(string, string) error, rollback func() error) (err error) {
	proxyBase := fmt.Sprintf("http://127.0.0.1:%d/", port)
	applied := false
	defer func() {
		if err != nil && applied {
			if rollbackErr := rollback(); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("roll back partial git proxy rewrites: %w", rollbackErr))
			}
		}
	}()

	if err := add(fmt.Sprintf("url.%sgithub.com/.insteadOf", proxyBase), "https://github.com/"); err != nil {
		return err
	}
	applied = true
	if addPush {
		// pushInsteadOf is a shared user key, so append our identity mapping
		// without replacing runner- or workflow-provided transport mappings,
		// and record ownership so cleanup removes only what we added.
		if err := add("url.https://github.com/.pushInsteadOf", "https://github.com/"); err != nil {
			return err
		}
		action.SetEnv(gitProxyAddedPushRewriteEnv, "true")
		os.Setenv(gitProxyAddedPushRewriteEnv, "true")
	}
	if clientToken != "" {
		header := gitproxy.BasicAuthHeader(clientToken)
		action.AddMask(header)
		if err := add(fmt.Sprintf("http.%s.extraheader", proxyBase), "AUTHORIZATION: "+header); err != nil {
			return err
		}
	}
	if action.GetInput("token") == "" {
		action.Warningf("No token input available: private repo mirroring will not work.")
	}
	return nil
}

func removeGitProxyRewrites() error {
	const (
		pattern   = `^(url\.http://127\.0\.0\.1:[0-9]+/github\.com/\.insteadof|url\.https://github\.com/\.pushinsteadof)$`
		proxyBase = "https://github.com/"
		pushKey   = "url.https://github.com/.pushinsteadof"
	)
	out, err := exec.Command("git", "config", "--global", "--get-regexp", pattern).Output()
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
	proxyPorts := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found || value != proxyBase {
			continue
		}
		if key == pushKey && os.Getenv(gitProxyAddedPushRewriteEnv) != "true" {
			// Never added by this action in this job: an identical value can
			// only be user-owned (or a harmless leftover no-op on a reused
			// machine), and duplicate values are indistinguishable.
			continue
		}
		if key != pushKey {
			const prefix = "url.http://127.0.0.1:"
			portAndSuffix := strings.TrimPrefix(key, prefix)
			if port, ok := strings.CutSuffix(portAndSuffix, "/github.com/.insteadof"); ok {
				proxyPorts[port] = true
			}
		}
		if err := gitConfigUnsetValue(key, value); err != nil {
			removeErr = errors.Join(removeErr, err)
		}
	}
	for port := range proxyPorts {
		key := fmt.Sprintf("http.http://127.0.0.1:%s/.extraheader", port)
		values, err := exec.Command("git", "config", "--global", "--get-all", key).Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				continue
			}
			removeErr = errors.Join(removeErr, fmt.Errorf("read git proxy header %s: %w", key, err))
			continue
		}
		for _, value := range strings.Split(strings.TrimSuffix(string(values), "\n"), "\n") {
			if strings.HasPrefix(strings.ToLower(value), "authorization: basic ") {
				if err := gitConfigUnsetValue(key, value); err != nil {
					removeErr = errors.Join(removeErr, err)
				}
			}
		}
	}
	return removeErr
}

func gitConfigUnsetValue(key, value string) error {
	output, err := exec.Command("git", "config", "--global", "--fixed-value", "--unset-all", key, value).CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 5) {
			return nil
		}
		return fmt.Errorf("git config --unset-all %s failed: %w\noutput: %s", key, err, output)
	}
	return nil
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
	if runtime.GOOS == "windows" {
		return nil
	}
	if state, err := gitproxy.ReadStateFile(gitProxyStateFile()); err == nil &&
		!shouldRestoreGitProxyRewrites(state, gitProxyOwner(), gitProxyHealthy(state.Port, os.Getenv(gitproxy.EnvHealthToken))) {
		return nil
	}
	return restoreGitProxyRewrites()
}

func shouldRestoreGitProxyRewrites(state gitproxy.State, owner string, healthy bool) bool {
	return state.Owner != owner || !healthy
}

func saveGitProxyPostState(action *githubactions.Action, state gitproxy.State) {
	action.SaveState(gitProxyPIDStateKey, strconv.Itoa(state.PID))
	action.SaveState(gitProxyMirrorDirStateKey, state.MirrorDir)
}

func loadGitProxyPostState() (gitproxy.State, bool, error) {
	pidValue := strings.TrimSpace(os.Getenv(gitProxyPIDStateEnv))
	mirrorDir := strings.TrimSpace(os.Getenv(gitProxyMirrorDirStateEnv))
	if pidValue == "" && mirrorDir == "" {
		return gitproxy.State{}, false, nil
	}
	if pidValue == "" || mirrorDir == "" {
		return gitproxy.State{}, false, fmt.Errorf("incomplete saved Git proxy state")
	}
	pid, err := strconv.Atoi(pidValue)
	if err != nil || pid <= 0 {
		return gitproxy.State{}, false, fmt.Errorf("invalid saved Git proxy PID %q", pidValue)
	}
	return gitproxy.State{PID: pid, MirrorDir: mirrorDir}, true, nil
}

// stopGit removes the URL rewrites then stops the proxy, in that order: git
// config must never point at a dead proxy. Runs in the post step, before the
// runner's job-completed hook unmounts and snapshots the sticky disk.
func stopGit(action *githubactions.Action) error {
	var stoppedState gitproxy.State
	err := restoreRewritesThenStop(restoreGitProxyRewrites, func() error {
		state, found, err := loadGitProxyPostState()
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if err := terminateGitProxy(action, state.PID); err != nil {
			return err
		}
		stoppedState = state
		return nil
	})
	if err != nil {
		return fmt.Errorf("git cache mode cleanup: %w", err)
	}
	if stoppedState.MirrorDir != "" {
		mountRoot := os.Getenv("RUNS_ON_STICKYDISK_DIR")
		if err := validateStickyMount(mountRoot); err != nil {
			return fmt.Errorf("git cache mode cleanup: validate sticky disk: %w", err)
		}
		mirrorDir := gitMirrorDir(mountRoot)
		if filepath.Clean(stoppedState.MirrorDir) != filepath.Clean(mirrorDir) {
			return fmt.Errorf("git cache mode cleanup: proxy mirror root %s does not match sticky disk mirror root %s", stoppedState.MirrorDir, mirrorDir)
		}
		removed, err := gitproxy.CleanupInterruptedRepacks(mirrorDir)
		if err != nil {
			return fmt.Errorf("git cache mode cleanup: remove interrupted repack files: %w", err)
		}
		if removed > 0 {
			action.Infof("Removed %d temporary Git pack file(s) left by interrupted repacks.", removed)
		}
	}

	if os.Getenv("RUNNER_DEBUG") == "1" {
		logProxyTail(action)
	}
	if err := removeFileIfExists(gitProxyLogFile()); err != nil {
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
	if err := forceKillGitProxy(pid); err != nil {
		return err
	}
	deadline = time.Now().Add(gitProxyKillWait)
	for time.Now().Before(deadline) {
		if !processAlive(proc) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("git proxy process group %d remained alive after forced termination", pid)
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
	// Replace rather than truncate: opening whatever already exists at this
	// path would write through a pre-planted file or symlink.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
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
	if log, err := readFileTail(gitProxyLogFile(), 4000); err == nil && log != "" {
		action.Infof("git proxy log:\n%s", log)
	}
}
