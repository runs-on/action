package stickydisk

import (
	"encoding/base64"
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
	return cfg
}

func globalConfigNames(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("git", "config", "--global", "--list").Output()
	return string(out)
}

func TestConfigureAndRemoveGitProxyRewrites(t *testing.T) {
	isolatedGitConfig(t)
	t.Setenv("INPUT_TOKEN", "test-token")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	if err := configureGitProxyRewrites(action, 8123); err != nil {
		t.Fatal(err)
	}

	cfg := globalConfigNames(t)
	wantB64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:test-token"))
	for _, want := range []string{
		"url.http://127.0.0.1:8123/github.com/.insteadof=https://github.com/",
		"url.https://github.com/.pushinsteadof=https://github.com/",
		"http.http://127.0.0.1:8123/.extraheader=AUTHORIZATION: basic " + wantB64,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("global config missing %q, got:\n%s", want, cfg)
		}
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

func TestConfigureGitProxyRewritesRollsBackPartialConfig(t *testing.T) {
	t.Setenv("INPUT_TOKEN", "test-token")
	action := githubactions.New(githubactions.WithWriter(os.Stderr))

	setCalls := 0
	addCalls := 0
	rollbackCalls := 0
	set := func(string, string) error {
		setCalls++
		return nil
	}
	add := func(string, string) error {
		addCalls++
		return os.ErrPermission
	}
	rollback := func() error {
		rollbackCalls++
		return nil
	}

	err := configureGitProxyRewritesWith(action, 8123, set, add, rollback)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("configure error = %v", err)
	}
	if rollbackCalls != 1 {
		t.Fatalf("rollback calls = %d, want 1", rollbackCalls)
	}
	if setCalls != 1 || addCalls != 1 {
		t.Fatalf("set/add calls = %d/%d, want 1/1", setCalls, addCalls)
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
