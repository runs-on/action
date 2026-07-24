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
if [ "$1 $2 $3 $4" = "buildx rm --force runs-on" ]; then
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
	if !strings.Contains(got, "buildx rm --force "+buildkitBuilderName) {
		t.Fatalf("surviving builder was not removed before volume reuse:\n%s", got)
	}
	if strings.Contains(got, "volume create") {
		t.Fatalf("matching volume was unnecessarily recreated:\n%s", got)
	}
}
