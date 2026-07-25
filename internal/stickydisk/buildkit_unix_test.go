//go:build !windows

package stickydisk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestPrepareBuildkitVolumeRemovesSurvivingBuilderBeforeReuse(t *testing.T) {
	bin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "docker.log")
	stateRoot := filepath.Join(t.TempDir(), "buildkit", "root")
	script := `#!/bin/sh
echo "$*" >> "$DOCKER_LOG"
if [ "$1 $2" = "volume inspect" ]; then
  printf '[{"Driver":"local","Labels":{"runs-on.stickydisk":"buildkit"},"Options":{"type":"none","o":"bind","device":"%s"}}]\n' "$STATE_ROOT"
  exit 0
fi
if [ "$1 $2 $3 $4 $5" = "buildx rm --force --keep-state runs-on" ]; then
	exit 0
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_LOG", logFile)
	t.Setenv("STATE_ROOT", stateRoot)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := prepareBuildkitVolume(githubactions.New(), stateRoot); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(log)
	if !strings.Contains(got, "buildx rm --force --keep-state "+buildkitBuilderName) {
		t.Fatalf("surviving builder was not removed before volume reuse:\n%s", got)
	}
	if strings.Contains(got, "volume create") {
		t.Fatalf("matching volume was unnecessarily recreated:\n%s", got)
	}
}

func TestPrepareBuildkitVolumeRecoversWhenBuilderMetadataIsMissing(t *testing.T) {
	bin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "docker.log")
	stateRoot := filepath.Join(t.TempDir(), "buildkit", "root")
	script := `#!/bin/sh
echo "$*" >> "$DOCKER_LOG"
if [ "$1 $2" = "volume inspect" ]; then
  printf '[{"Driver":"local","Labels":{"runs-on.stickydisk":"buildkit"},"Options":{"type":"none","o":"bind","device":"%s"}}]\n' "$STATE_ROOT"
  exit 0
fi
if [ "$1 $2 $3 $4 $5" = "buildx rm --force --keep-state runs-on" ]; then
  echo 'failed to remove runs-on: no builder "runs-on" found' >&2
  exit 1
fi
if [ "$1 $2 $3" = "rm --force buildx_buildkit_runs-on0" ]; then
  echo 'Error response from daemon: No such container: buildx_buildkit_runs-on0' >&2
  exit 1
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_LOG", logFile)
	t.Setenv("STATE_ROOT", stateRoot)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := prepareBuildkitVolume(githubactions.New(), stateRoot); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(log)
	if !strings.Contains(got, "rm --force "+buildkitContainerName) {
		t.Fatalf("orphaned BuildKit container cleanup was not attempted:\n%s", got)
	}
	if strings.Contains(got, "volume create") {
		t.Fatalf("matching volume was unnecessarily recreated:\n%s", got)
	}
}

func TestCleanupBuildkitTreatsCompletedForcedRemovalAsSafe(t *testing.T) {
	previousState, previousErr := os.ReadFile(buildkitPreparedStateFile)
	t.Cleanup(func() {
		if previousErr == nil {
			_ = os.WriteFile(buildkitPreparedStateFile, previousState, 0o600)
		} else {
			_ = os.Remove(buildkitPreparedStateFile)
		}
	})

	bin := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "buildkit", "root")
	script := `#!/bin/sh
case "$1 $2" in
  "buildx ls")
    printf '%s\n' "runs-on0"
    ;;
  "container inspect")
    printf '[{"Mounts":[{"Type":"volume","Name":"buildx_buildkit_runs-on0_state","Destination":"/var/lib/buildkit"}]}]\n'
    ;;
  "volume inspect")
    printf '[{"Driver":"local","Labels":{"runs-on.stickydisk":"buildkit"},"Options":{"type":"none","o":"bind","device":"%s"}}]\n' "$VOLUME_DEVICE"
    ;;
  "stop --time")
    exit 1
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(buildkitPreparedStateFile, []byte(stateRoot), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VOLUME_DEVICE", filepath.Join(t.TempDir(), "not-sticky"))

	safeToDelete, err := cleanupBuildkitWithSafety(githubactions.New())
	if !safeToDelete {
		t.Fatal("successful forced builder/container and volume removal should permit pressure recovery")
	}
	if err == nil || !strings.Contains(err.Error(), "is not backed by") || !strings.Contains(err.Error(), "did not stop cleanly") {
		t.Fatalf("cleanup error = %v, want verification and graceful-stop errors", err)
	}
}
