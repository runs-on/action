//go:build !windows

package stickydisk

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestAptConfigurationRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		dockerClean  string
		keepArchives string
		wantDocker   bool
		wantKeep     bool
	}{
		{
			name:        "restore docker-clean and remove action config",
			dockerClean: "APT::Update::Post-Invoke { \"rm cached packages\"; };\n",
			wantDocker:  true,
		},
		{
			name:         "restore pre-existing keep config",
			keepArchives: "APT::Keep-Downloaded-Packages \"false\";\n",
			wantKeep:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			archivesDir := filepath.Join(root, "archives")
			configDir := filepath.Join(root, "apt.conf.d")
			stateTempDir := filepath.Join(root, "state")
			for _, dir := range []string{configDir, stateTempDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			dockerCleanPath := filepath.Join(configDir, aptDockerCleanName)
			keepArchivesPath := filepath.Join(configDir, aptKeepArchivesName)
			if tt.dockerClean != "" {
				if err := os.WriteFile(dockerCleanPath, []byte(tt.dockerClean), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if tt.keepArchives != "" {
				if err := os.WriteFile(keepArchivesPath, []byte(tt.keepArchives), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			bin := t.TempDir()
			sudo := filepath.Join(bin, "sudo")
			if err := os.WriteFile(sudo, []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			stateFile := filepath.Join(root, "github-state")
			t.Setenv("GITHUB_STATE", stateFile)

			action := githubactions.New(githubactions.WithWriter(io.Discard))
			if err := configureAptAt(action, archivesDir, configDir, stateTempDir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dockerCleanPath); !os.IsNotExist(err) {
				t.Fatalf("docker-clean remains after setup: %v", err)
			}
			gotKeep, err := os.ReadFile(keepArchivesPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotKeep) != aptKeepArchivesConfig {
				t.Fatalf("active keep config = %q, want %q", gotKeep, aptKeepArchivesConfig)
			}

			snapshots, err := os.ReadDir(stateTempDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshots) != 1 {
				t.Fatalf("snapshot dirs = %d, want 1", len(snapshots))
			}
			snapshotRoot := filepath.Join(stateTempDir, snapshots[0].Name())
			state, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(state), snapshotRoot) {
				t.Fatalf("saved action state does not contain snapshot path %s: %s", snapshotRoot, state)
			}

			t.Setenv(aptConfigSnapshotStateEnv, snapshotRoot)
			if err := restoreAptAt(action, configDir, stateTempDir); err != nil {
				t.Fatal(err)
			}
			assertAptConfigFile(t, dockerCleanPath, tt.dockerClean, tt.wantDocker)
			assertAptConfigFile(t, keepArchivesPath, tt.keepArchives, tt.wantKeep)
			if _, err := os.Stat(snapshotRoot); !os.IsNotExist(err) {
				t.Fatalf("snapshot remains after restore: %v", err)
			}
		})
	}
}

func assertAptConfigFile(t *testing.T, path, want string, exists bool) {
	t.Helper()
	got, err := os.ReadFile(path)
	if !exists {
		if !os.IsNotExist(err) {
			t.Fatalf("%s exists after restore: %v", path, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
