//go:build !windows

package stickydisk

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestAptConfigurationRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RUNNER_TEMP", t.TempDir())
	archivesDir := filepath.Join(root, "archives")
	configDir := filepath.Join(root, "apt.conf.d")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerCleanPath := filepath.Join(configDir, aptDockerCleanName)
	dockerClean := "APT::Update::Post-Invoke { \"rm cached packages\"; };\n"
	if err := os.WriteFile(dockerCleanPath, []byte(dockerClean), 0o640); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	action := githubactions.New(githubactions.WithWriter(io.Discard))
	if err := configureAptAt(action, archivesDir, configDir); err != nil {
		t.Fatal(err)
	}
	keepArchivesPath := filepath.Join(configDir, aptKeepArchivesName)
	if got, err := os.ReadFile(keepArchivesPath); err != nil || string(got) != aptKeepArchivesConfig {
		t.Fatalf("active keep config = %q, err = %v", got, err)
	}
	if _, err := os.Stat(dockerCleanPath); !os.IsNotExist(err) {
		t.Fatalf("docker-clean still active: %v", err)
	}
	if got, err := os.ReadFile(aptDockerCleanBackup()); err != nil || string(got) != dockerClean {
		t.Fatalf("parked docker-clean = %q, err = %v", got, err)
	}

	// A pre-existing .disabled file owned by the image or workflow must
	// survive the round trip untouched.
	userDisabled := filepath.Join(configDir, aptDockerCleanName+".disabled")
	if err := os.WriteFile(userDisabled, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreAptAt(action, configDir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dockerCleanPath); err != nil || string(got) != dockerClean {
		t.Fatalf("restored docker-clean = %q, err = %v", got, err)
	}
	if _, err := os.Stat(keepArchivesPath); !os.IsNotExist(err) {
		t.Fatalf("action apt config remains after restore: %v", err)
	}
	if got, err := os.ReadFile(userDisabled); err != nil || string(got) != "user-owned" {
		t.Fatalf("user-owned .disabled file was modified: %q, err = %v", got, err)
	}
}
