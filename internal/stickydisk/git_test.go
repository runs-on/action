package stickydisk

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runs-on/action/internal/gitproxy"
	"github.com/sethvargo/go-githubactions"
)

// isolatedGitConfig points the global git config at a temp file so tests
// never touch the developer's real configuration.
func isolatedGitConfig(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir() + "/gitconfig"
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// action.SetEnv writes to the GITHUB_ENV file and panics without one.
	t.Setenv("GITHUB_ENV", filepath.Join(t.TempDir(), "github_env"))
	return cfg
}

func globalConfigNames(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("git", "config", "--global", "--list").Output()
	return string(out)
}

// requireGit skips tests that exercise real git config or repositories on
// images without git (e.g. windows22-base); the production code tolerates a
// missing git binary the same way.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func TestConfigureAndRemoveGitProxyRewrites(t *testing.T) {
	requireGit(t)
	isolatedGitConfig(t)
	t.Setenv("INPUT_TOKEN", "test-token")
	t.Setenv(gitProxyAddedPushRewriteEnv, "")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	if err := configureGitProxyRewrites(action, 8123, "opaque-client"); err != nil {
		t.Fatal(err)
	}

	cfg := globalConfigNames(t)
	for _, want := range []string{
		"url.http://127.0.0.1:8123/github.com/.insteadof=https://github.com/",
		"url.https://github.com/.pushinsteadof=https://github.com/",
		"http.http://127.0.0.1:8123/.extraheader=AUTHORIZATION: " + gitproxy.BasicAuthHeader("opaque-client"),
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("global config missing %q, got:\n%s", want, cfg)
		}
	}
	// The real GitHub credential must never enter git config in any form:
	// later checkout-controlled steps can read global config.
	if strings.Contains(cfg, "test-token") || strings.Contains(cfg, gitproxy.BasicAuthHeader("test-token")) {
		t.Errorf("real token leaked into git config:\n%s", cfg)
	}

	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
	}
	cfg = globalConfigNames(t)
	for _, gone := range []string{"127.0.0.1", "pushinsteadof"} {
		if strings.Contains(cfg, gone) {
			t.Errorf("global config still contains %q after cleanup:\n%s", gone, cfg)
		}
	}
}

func TestRemoveGitProxyRewritesPreservesUserOwnedValues(t *testing.T) {
	requireGit(t)
	isolatedGitConfig(t)
	t.Setenv("INPUT_TOKEN", "test-token")
	t.Setenv(gitProxyAddedPushRewriteEnv, "")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	const (
		pushKey       = "url.https://github.com/.pushInsteadOf"
		userPushValue = "ssh://git@github.com/"
		proxyKey      = "http.http://127.0.0.1:8123/.extraheader"
		userHeader    = "X-Workflow-Proxy: keep"
		otherKey      = "http.http://127.0.0.1:9000/.extraheader"
		otherHeader   = "AUTHORIZATION: basic unrelated"
	)
	for _, kv := range [][2]string{
		{pushKey, userPushValue},
		{proxyKey, userHeader},
		{otherKey, otherHeader},
	} {
		if err := gitConfigAdd(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := configureGitProxyRewrites(action, 8123, "opaque-client"); err != nil {
		t.Fatal(err)
	}
	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
	}

	cfg := globalConfigNames(t)
	for _, want := range []string{
		"pushinsteadof=" + userPushValue,
		"extraheader=" + userHeader,
		"extraheader=" + otherHeader,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cleanup removed user-owned config %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "insteadof=https://github.com/") ||
		strings.Contains(strings.ToLower(cfg), "authorization: basic e") {
		t.Errorf("action-owned rewrites survived cleanup:\n%s", cfg)
	}
}

func TestConfigureGitProxyRewritesRollsBackPartialConfig(t *testing.T) {
	t.Setenv("INPUT_TOKEN", "test-token")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	addCalls := 0
	rollbackCalls := 0
	add := func(string, string) error {
		addCalls++
		if addCalls == 2 {
			return os.ErrPermission
		}
		return nil
	}
	rollback := func() error {
		rollbackCalls++
		return nil
	}

	err := configureGitProxyRewritesWith(action, 8123, "opaque-client", true, add, rollback)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("configure error = %v", err)
	}
	if rollbackCalls != 1 {
		t.Fatalf("rollback calls = %d, want 1", rollbackCalls)
	}
	if addCalls != 2 {
		t.Fatalf("add calls = %d, want 2", addCalls)
	}
}

func TestCommandLineHasGitProxyServe(t *testing.T) {
	if !commandLineHasGitProxyServe("/path/action\x00--git-proxy-serve\x00") {
		t.Fatal("proxy command line was not recognized")
	}
	if commandLineHasGitProxyServe("/path/action --git-proxy-server") {
		t.Fatal("unrelated process command line was accepted")
	}
}

func TestGitProxyLogLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git-proxy.log")
	if err := os.WriteFile(path, []byte("prior job\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	logFile, err := openGitProxyLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("current job\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "current job\n" {
		t.Fatalf("truncated log = %q, err = %v", got, err)
	}

	large := strings.Repeat("x", 8192) + "tail"
	if err := os.WriteFile(path, []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := readFileTail(path, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) > 4003 || !strings.HasPrefix(tail, "...") || !strings.HasSuffix(tail, "tail") {
		t.Fatalf("bounded tail = %q (len %d)", tail, len(tail))
	}

	if err := removeFileIfExists(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("proxy log still exists after cleanup: %v", err)
	}
}

func TestRestoreRewritesBeforeStoppingProxy(t *testing.T) {
	restoreErr := errors.New("git config is locked")
	stopCalls := 0
	err := restoreRewritesThenStop(
		func() error { return restoreErr },
		func() error {
			stopCalls++
			return nil
		},
	)
	if !errors.Is(err, restoreErr) {
		t.Fatalf("cleanup error = %v, want %v", err, restoreErr)
	}
	if stopCalls != 0 {
		t.Fatalf("proxy stop called %d times after rewrite restoration failed", stopCalls)
	}

	err = restoreRewritesThenStop(
		func() error { return nil },
		func() error {
			stopCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 {
		t.Fatalf("proxy stop calls = %d, want 1 after successful restoration", stopCalls)
	}
}

// removeGitProxyRewrites must be a no-op (not an error) when nothing was
// configured: the post step always runs, even after a failed setup.
func TestRemoveGitProxyRewritesIdempotent(t *testing.T) {
	isolatedGitConfig(t)
	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
	}
}

func TestGitProxyStateMatchesMirrorRoot(t *testing.T) {
	state := gitproxy.State{MirrorDir: "/mnt/sticky/git/mirrors", Owner: "123/1/build"}
	if !gitProxyStateMatches(state, "/mnt/sticky/git/mirrors", "123/1/build") {
		t.Fatal("matching mirror root was rejected")
	}
	if gitProxyStateMatches(state, "/mnt/other/git/mirrors", "123/1/build") {
		t.Fatal("stale mirror root was accepted")
	}
	if gitProxyStateMatches(state, "/mnt/sticky/git/mirrors", "124/1/build") {
		t.Fatal("proxy from another job was accepted")
	}
	if gitProxyStateMatches(gitproxy.State{}, "/mnt/sticky/git/mirrors", "123/1/build") {
		t.Fatal("legacy state without mirror root was accepted")
	}
}

func TestGitProxyPostStateSurvivesMissingRuntimeStateFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "github-state")
	t.Setenv("GITHUB_STATE", stateFile)
	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	want := gitproxy.State{PID: 4321, MirrorDir: "/mnt/sticky/git/mirrors"}

	saveGitProxyPostState(action, want)
	contents, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{gitProxyPIDStateKey, "4321", gitProxyMirrorDirStateKey, want.MirrorDir} {
		if !strings.Contains(string(contents), value) {
			t.Fatalf("saved post state %q does not contain %q", contents, value)
		}
	}

	// The mutable runtime state file is deliberately absent. GitHub exposes
	// saved action state directly to this invocation's post step.
	t.Setenv(gitProxyPIDStateEnv, "4321")
	t.Setenv(gitProxyMirrorDirStateEnv, want.MirrorDir)
	got, found, err := loadGitProxyPostState()
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.PID != want.PID || got.MirrorDir != want.MirrorDir {
		t.Fatalf("loaded post state = (%+v, %t), want (%+v, true)", got, found, want)
	}
}

func TestLoadGitProxyPostStateRejectsInvalidPID(t *testing.T) {
	t.Setenv(gitProxyPIDStateEnv, "not-a-pid")
	t.Setenv(gitProxyMirrorDirStateEnv, "/mnt/sticky/git/mirrors")
	if _, _, err := loadGitProxyPostState(); err == nil {
		t.Fatal("invalid saved proxy PID was accepted")
	}
}

func TestGitProxyOwnerDistinguishesMatrixExecutions(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	t.Setenv("GITHUB_JOB", "test")
	t.Setenv("RUNNER_TRACKING_ID", "matrix-leg-a")
	first := gitProxyOwner()

	t.Setenv("RUNNER_TRACKING_ID", "matrix-leg-b")
	second := gitProxyOwner()
	if first == second {
		t.Fatalf("matrix executions share proxy owner %q", first)
	}
}

func TestShouldRestoreGitProxyRewrites(t *testing.T) {
	state := gitproxy.State{Owner: "123/1/build"}
	if shouldRestoreGitProxyRewrites(state, "123/1/build", true) {
		t.Fatal("healthy current-job proxy rewrites were treated as stale")
	}
	if !shouldRestoreGitProxyRewrites(state, "other/1/build", true) {
		t.Fatal("prior-job proxy rewrites were retained")
	}
	if !shouldRestoreGitProxyRewrites(state, "123/1/build", false) {
		t.Fatal("unhealthy current-job proxy rewrites were retained")
	}
}

func TestGitMirrorCacheHitRequiresBareRepository(t *testing.T) {
	requireGit(t)
	mirrorDir := t.TempDir()
	repoPath := filepath.Join(mirrorDir, "github.com", "owner", "repo.git")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if gitMirrorCacheHit(mirrorDir, "owner/repo") {
		t.Fatal("non-repository mirror reported a cache hit")
	}
	if out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, out)
	}
	if !gitMirrorCacheHit(mirrorDir, "owner/repo") {
		t.Fatal("valid bare mirror did not report a cache hit")
	}
	if !gitMirrorCacheHit(mirrorDir, "Owner/RePo") {
		t.Fatal("mixed-case GitHub repository did not reuse lowercase mirror")
	}

	worktree := filepath.Join(t.TempDir(), "worktree")
	if out, err := exec.Command("git", "init", worktree).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktree, "cached.txt"), []byte("cached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", worktree, "config", "user.name", "test"},
		{"-C", worktree, "config", "user.email", "test@example.com"},
		{"-C", worktree, "add", "cached.txt"},
		{"-C", worktree, "-c", "commit.gpgsign=false", "commit", "-m", "cached"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "clone", "--mirror", worktree, repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git clone --mirror failed: %v\n%s", err, out)
	}
	blobOut, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD:cached.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("resolve cached blob failed: %v\n%s", err, blobOut)
	}
	blob := strings.TrimSpace(string(blobOut))
	if err := os.Remove(filepath.Join(repoPath, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatalf("remove reachable mirror object: %v", err)
	}
	if gitMirrorCacheHit(mirrorDir, "owner/repo") {
		t.Fatal("mirror with a missing reachable object reported a cache hit")
	}
}

// A failed or skipped setup must leave the global git config untouched.
func TestSetupGitSkipsGHES(t *testing.T) {
	cfgFile := isolatedGitConfig(t)
	t.Setenv("GITHUB_SERVER_URL", "https://ghes.example.com")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	hit, err := setupGit(action, t.TempDir())
	if err != nil || hit {
		t.Fatalf("setupGit = (%v, %v), want (false, nil)", hit, err)
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Errorf("global git config was written on GHES skip")
	}
}

func TestUpstreamTokenDigest(t *testing.T) {
	if upstreamTokenDigest("token-a") == upstreamTokenDigest("token-b") {
		t.Fatal("different tokens share a digest")
	}
	if upstreamTokenDigest("token-a") != upstreamTokenDigest("token-a") {
		t.Fatal("digest is not deterministic")
	}
	if upstreamTokenDigest("") == upstreamTokenDigest("token-a") {
		t.Fatal("empty token shares a digest with a real one")
	}
}

// The long-lived proxy must not inherit credential-bearing variables: its
// /proc/<pid>/environ is readable by any same-uid process for the whole job.
func TestProxyChildEnvironStripsCredentials(t *testing.T) {
	t.Setenv("INPUT_TOKEN", "ghs_secret")
	t.Setenv("INPUT_STICKY_CACHE", "git")
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "runtime-secret")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-secret")
	t.Setenv("GITHUB_TOKEN", "ghs_secret2")
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")

	for _, kv := range proxyChildEnviron() {
		for _, banned := range []string{"INPUT_", "ACTIONS_RUNTIME_TOKEN=", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=", "GITHUB_TOKEN="} {
			if strings.HasPrefix(kv, banned) {
				t.Errorf("proxy environment leaks %s", kv)
			}
		}
	}
	kept := false
	for _, kv := range proxyChildEnviron() {
		if kv == "GITHUB_REPOSITORY=owner/repo" {
			kept = true
		}
	}
	if !kept {
		t.Error("non-sensitive variables must survive filtering")
	}
}

// A user-maintained identity pushInsteadOf mapping is indistinguishable from
// the action's own by value; ownership is tracked out of band, and cleanup
// must leave the user's copy alone when the action never added one.
func TestRemoveGitProxyRewritesPreservesUserOwnedIdentityMapping(t *testing.T) {
	requireGit(t)
	isolatedGitConfig(t)
	t.Setenv("INPUT_TOKEN", "test-token")
	t.Setenv(gitProxyAddedPushRewriteEnv, "")

	if err := gitConfigAdd("url.https://github.com/.pushInsteadOf", "https://github.com/"); err != nil {
		t.Fatal(err)
	}
	action := githubactions.New(githubactions.WithWriter(os.Stderr))
	if err := configureGitProxyRewrites(action, 8123, "opaque-client"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(gitProxyAddedPushRewriteEnv) == "true" {
		t.Fatal("action claimed ownership of a pre-existing identity mapping")
	}
	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
	}

	cfg := globalConfigNames(t)
	if !strings.Contains(cfg, "pushinsteadof=https://github.com/") {
		t.Errorf("user-owned identity mapping was removed:\n%s", cfg)
	}
	if strings.Contains(cfg, "127.0.0.1") {
		t.Errorf("proxy rewrites survived cleanup:\n%s", cfg)
	}
}

// setupGit removes an action-owned push rewrite at the start of every
// invocation; a repeated invocation (e.g. the token-change proxy restart)
// must re-add it or pushes would route through the proxy and fail.
func TestPushRewriteReaddedAfterOwnedCleanup(t *testing.T) {
	requireGit(t)
	isolatedGitConfig(t)
	t.Setenv("INPUT_TOKEN", "test-token")
	t.Setenv(gitProxyAddedPushRewriteEnv, "")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	if err := configureGitProxyRewrites(action, 8123, "opaque-client"); err != nil {
		t.Fatal(err)
	}
	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
	}
	if err := configureGitProxyRewrites(action, 8124, "opaque-client"); err != nil {
		t.Fatal(err)
	}

	cfg := globalConfigNames(t)
	if !strings.Contains(cfg, "pushinsteadof=https://github.com/") {
		t.Errorf("push rewrite missing after repeated setup:\n%s", cfg)
	}
}
