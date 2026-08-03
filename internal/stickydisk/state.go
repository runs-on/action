package stickydisk

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

const (
	stickyDiskDirEnv             = "RUNS_ON_STICKYDISK_DIR"
	stickyDiskReadyFileEnv       = "RUNS_ON_STICKYDISK_READY_FILE"
	stickyDiskUnavailableFileEnv = "RUNS_ON_STICKYDISK_UNAVAILABLE_FILE"
	stickyDiskTimingsFileEnv     = "RUNS_ON_STICKYDISK_TIMINGS_FILE"
	stickyDiskNameEnv            = "RUNS_ON_STICKYDISK_NAME"
	jobCacheStateEnv             = "RUNS_ON_STICKY_CACHE_STATE"
)

type stickyDiskContract struct {
	MountRoot       string
	ReadyFile       string
	UnavailableFile string
}

func readStickyDiskContract() (stickyDiskContract, error) {
	contract := stickyDiskContract{
		MountRoot:       strings.TrimSpace(os.Getenv(stickyDiskDirEnv)),
		ReadyFile:       strings.TrimSpace(os.Getenv(stickyDiskReadyFileEnv)),
		UnavailableFile: strings.TrimSpace(os.Getenv(stickyDiskUnavailableFileEnv)),
	}
	var missing []string
	for _, value := range []struct {
		name  string
		value string
	}{
		{stickyDiskDirEnv, contract.MountRoot},
		{stickyDiskReadyFileEnv, contract.ReadyFile},
		{stickyDiskUnavailableFileEnv, contract.UnavailableFile},
	} {
		if value.value == "" {
			missing = append(missing, value.name)
		}
	}
	if len(missing) > 0 {
		return stickyDiskContract{}, fmt.Errorf("sticky disk agent contract is incomplete: %s not set", strings.Join(missing, ", "))
	}
	return contract, nil
}

// jobCacheState preserves the original hit result across action invocations
// in one job. Mount paths and setup-owned modes are separate domains so paths
// never need reserved prefixes.
type jobCacheState struct {
	Mounts map[string]bool `json:"mounts"`
	Modes  map[string]bool `json:"modes"`
}

func readJobCacheState() (jobCacheState, error) {
	state := jobCacheState{
		Mounts: map[string]bool{},
		Modes:  map[string]bool{},
	}
	value := strings.TrimSpace(os.Getenv(jobCacheStateEnv))
	if value == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return jobCacheState{}, err
	}
	if state.Mounts == nil {
		state.Mounts = map[string]bool{}
	}
	if state.Modes == nil {
		state.Modes = map[string]bool{}
	}
	return state, nil
}

func writeJobCacheState(action *githubactions.Action, state jobCacheState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	action.SetEnv(jobCacheStateEnv, string(data))
	return nil
}

func activeCacheTargets(mountRoot string) ([]string, error) {
	targets, err := mountedCacheTargets(mountRoot)
	if err != nil {
		return nil, err
	}
	state, err := readJobCacheState()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(targets)+len(state.Mounts))
	for _, target := range targets {
		seen[canonicalPath(target)] = true
	}
	trackedTargets := make([]string, 0, len(state.Mounts))
	for target := range state.Mounts {
		trackedTargets = append(trackedTargets, target)
	}
	slices.Sort(trackedTargets)
	for _, target := range trackedTargets {
		if key := canonicalPath(target); !seen[key] {
			targets = append(targets, target)
			seen[key] = true
		}
	}
	return targets, nil
}
