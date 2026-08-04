package stickydisk

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestReadStickyDiskContractRequiresAgentEnvironment(t *testing.T) {
	t.Setenv(stickyDiskDirEnv, "/mnt/sticky")
	t.Setenv(stickyDiskReadyFileEnv, "")
	t.Setenv(stickyDiskUnavailableFileEnv, "")

	_, err := readStickyDiskContract()
	if err == nil || !strings.Contains(err.Error(), "RUNS_ON_STICKYDISK_READY_FILE, RUNS_ON_STICKYDISK_UNAVAILABLE_FILE not set") {
		t.Fatalf("incomplete agent contract error = %v", err)
	}
}

func TestReadStickyDiskContract(t *testing.T) {
	t.Setenv(stickyDiskDirEnv, "/mnt/sticky")
	t.Setenv(stickyDiskReadyFileEnv, "/runs-on/sticky.ready")
	t.Setenv(stickyDiskUnavailableFileEnv, "/runs-on/sticky.unavailable")

	contract, err := readStickyDiskContract()
	if err != nil {
		t.Fatal(err)
	}
	if contract.MountRoot != "/mnt/sticky" || contract.ReadyFile != "/runs-on/sticky.ready" || contract.UnavailableFile != "/runs-on/sticky.unavailable" {
		t.Fatalf("agent contract = %#v", contract)
	}
}

func TestReadJobCacheStateSeparatesMountsAndModes(t *testing.T) {
	t.Setenv(jobCacheStateEnv, `{"mounts":{"/home/runner/.cache/go-build":false},"modes":{"git":true}}`)

	state, err := readJobCacheState()
	if err != nil {
		t.Fatal(err)
	}
	if hit, found := state.Mounts["/home/runner/.cache/go-build"]; !found || hit {
		t.Fatalf("mount state = %v", state.Mounts)
	}
	if hit, found := state.Modes["git"]; !found || !hit {
		t.Fatalf("mode state = %v", state.Modes)
	}
}

func TestReadJobCacheStateInitializesEmptyDomains(t *testing.T) {
	t.Setenv(jobCacheStateEnv, `{}`)

	state, err := readJobCacheState()
	if err != nil {
		t.Fatal(err)
	}
	state.Mounts["/cache"] = true
	state.Modes["buildkit"] = false
}

func TestWriteJobCacheStatePublishesTypedDomains(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "github-env")
	t.Setenv("GITHUB_ENV", envFile)
	action := githubactions.New(githubactions.WithWriter(io.Discard))

	err := writeJobCacheState(action, jobCacheState{
		Mounts: map[string]bool{"/cache": false},
		Modes:  map[string]bool{"git": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{jobCacheStateEnv, `"mounts":{"/cache":false}`, `"modes":{"git":true}`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("published state missing %q:\n%s", want, data)
		}
	}
}
