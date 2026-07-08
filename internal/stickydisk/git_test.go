package stickydisk

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"

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

// removeGitProxyRewrites must be a no-op (not an error) when nothing was
// configured: the post step always runs, even after a failed setup.
func TestRemoveGitProxyRewritesIdempotent(t *testing.T) {
	isolatedGitConfig(t)
	if err := removeGitProxyRewrites(); err != nil {
		t.Fatal(err)
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
