package stickydisk

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

const (
	buildkitBuilderName       = "runs-on"
	buildkitNodeName          = buildkitBuilderName + "0"
	buildkitContainerName     = "buildx_buildkit_" + buildkitNodeName
	buildkitStateVolumeName   = buildkitContainerName + "_state"
	buildkitStateTarget       = "/var/lib/buildkit"
	buildkitPreparedStateFile = "/tmp/runs-on-buildkit-volume"
	buildkitStopWait          = 20 * time.Second
	buildkitVolumeLabel       = "runs-on.stickydisk=buildkit"
)

type dockerVolumeInspect struct {
	Driver  string            `json:"Driver"`
	Labels  map[string]string `json:"Labels"`
	Options map[string]string `json:"Options"`
}

type dockerMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Destination string `json:"Destination"`
}

type dockerContainerInspect struct {
	Mounts []dockerMount `json:"Mounts"`
}

// SetBuildkitOutputs publishes the stable builder name and the platform-owned
// BuildKit configuration even when sticky-disk caching is not requested.
func SetBuildkitOutputs(action *githubactions.Action) {
	action.SetOutput("buildkit-builder", buildkitBuilderName)

	config, err := buildkitInlineConfigFromEnv()
	if err != nil {
		action.Warningf("Cannot configure the BuildKit ECR pull-through mirror: %v", err)
		config = ""
	}
	action.SetOutput("buildkit-inline-config", config)
}

func buildkitInlineConfigFromEnv() (string, error) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("RUNS_ON_ECR_PULL_THROUGH_CACHE_DOCKER_HUB_MIRROR")), "true") {
		return "", nil
	}

	mirror, err := normalizeBuildkitMirror(os.Getenv("RUNS_ON_ECR_PULL_THROUGH_CACHE"))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[registry.\"docker.io\"]\n  mirrors = [%q]\n", mirror), nil
}

func normalizeBuildkitMirror(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("RUNS_ON_ECR_PULL_THROUGH_CACHE is empty")
	}

	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid ECR registry %q: %w", value, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid ECR registry %q: expected an HTTPS registry host without credentials, path, query, or fragment", value)
	}
	if strings.ContainsAny(parsed.Host, "\r\n\t \"'") {
		return "", fmt.Errorf("invalid ECR registry host %q", parsed.Host)
	}
	return parsed.Host, nil
}

// setupBuildkit pre-creates the state volume expected by Buildx's single-node
// docker-container driver. This intentionally follows Buildx's
// buildx_buildkit_<node>_state naming contract; post-job verification turns an
// upstream naming change into a visible failure instead of an ephemeral cache.
// Contract source: https://github.com/docker/buildx/blob/v0.34.1/driver/docker-container/driver.go
func setupBuildkit(action *githubactions.Action, mountRoot string) (hit bool, err error) {
	stateRoot := filepath.Join(mountRoot, "buildkit", "root")
	hit = dirNonEmpty(stateRoot)
	if err := os.MkdirAll(stateRoot, 0755); err != nil {
		return hit, fmt.Errorf("failed to create %s: %w", stateRoot, err)
	}

	if err := prepareBuildkitVolume(action, stateRoot); err != nil {
		return hit, err
	}
	if err := os.WriteFile(buildkitPreparedStateFile, []byte(stateRoot), 0600); err != nil {
		return hit, fmt.Errorf("record prepared BuildKit volume: %w", err)
	}

	action.Infof("Prepared sticky BuildKit state volume '%s' for builder '%s'. Run docker/setup-buildx-action next with name=%s, driver=docker-container, and cleanup=false.", buildkitStateVolumeName, buildkitBuilderName, buildkitBuilderName)
	return hit, nil
}

func prepareBuildkitVolume(action *githubactions.Action, stateRoot string) error {
	volume, found, err := inspectDockerVolume(buildkitStateVolumeName)
	if err != nil {
		return err
	}
	if found {
		if !dockerVolumeMatches(volume, stateRoot) {
			return fmt.Errorf("Docker volume %s already exists but is not backed by %s; run runs-on/action before docker/setup-buildx-action", buildkitStateVolumeName, stateRoot)
		}
		action.Infof("Reusing sticky BuildKit state volume '%s'.", buildkitStateVolumeName)
		return nil
	}

	args := buildkitVolumeCreateArgs(stateRoot)
	if err := runLogged(action, "docker", args...); err != nil {
		return fmt.Errorf("create sticky BuildKit state volume: %w", err)
	}
	return nil
}

func buildkitVolumeCreateArgs(stateRoot string) []string {
	return []string{
		"volume", "create",
		"--driver", "local",
		"--label", buildkitVolumeLabel,
		"--opt", "type=none",
		"--opt", "o=bind",
		"--opt", "device=" + stateRoot,
		buildkitStateVolumeName,
	}
}

func inspectDockerVolume(name string) (dockerVolumeInspect, bool, error) {
	out, err := exec.Command("docker", "volume", "inspect", name).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such volume") {
			return dockerVolumeInspect{}, false, nil
		}
		return dockerVolumeInspect{}, false, fmt.Errorf("inspect Docker volume %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}

	var volumes []dockerVolumeInspect
	if err := json.Unmarshal(out, &volumes); err != nil {
		return dockerVolumeInspect{}, false, fmt.Errorf("decode Docker volume %s inspection: %w", name, err)
	}
	if len(volumes) != 1 {
		return dockerVolumeInspect{}, false, fmt.Errorf("decode Docker volume %s inspection: expected one result, got %d", name, len(volumes))
	}
	return volumes[0], true, nil
}

func dockerVolumeMatches(volume dockerVolumeInspect, stateRoot string) bool {
	return volume.Driver == "local" &&
		volume.Options["type"] == "none" &&
		volume.Options["o"] == "bind" &&
		filepath.Clean(volume.Options["device"]) == filepath.Clean(stateRoot)
}

// cleanupBuildkit verifies that the official setup action used the prepared
// volume, then owns final shutdown because the sticky disk must not be
// snapshotted while BuildKit is still writing to it.
func cleanupBuildkit(action *githubactions.Action) error {
	stateRootBytes, err := os.ReadFile(buildkitPreparedStateFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prepared BuildKit state: %w", err)
	}
	defer os.Remove(buildkitPreparedStateFile)
	stateRoot := strings.TrimSpace(string(stateRootBytes))

	container, err := inspectBuildkitContainer()
	if err != nil {
		return err
	}
	if !containerUsesBuildkitVolume(container) {
		return fmt.Errorf("Buildx container %s did not mount expected volume %s at %s; the pinned Buildx volume contract may have changed", buildkitContainerName, buildkitStateVolumeName, buildkitStateTarget)
	}

	action.Infof("Verified sticky BuildKit state mount: %s -> %s", stateRoot, buildkitStateTarget)
	stopErr := runLogged(action, "docker", "stop", "--time", fmt.Sprintf("%.0f", buildkitStopWait.Seconds()), buildkitContainerName)
	rmErr := runLogged(action, "docker", "buildx", "rm", "--force", buildkitBuilderName)
	if rmErr != nil {
		action.Warningf("Buildx cleanup failed, removing its container directly: %v", rmErr)
		if fallbackErr := runLogged(action, "docker", "rm", "--force", buildkitContainerName); fallbackErr != nil {
			return fmt.Errorf("remove BuildKit container after Buildx cleanup failed: %w", fallbackErr)
		}
	}
	if err := removeDockerVolume(buildkitStateVolumeName); err != nil {
		return err
	}
	if stopErr != nil {
		return fmt.Errorf("BuildKit did not stop cleanly before forced cleanup: %w", stopErr)
	}
	return nil
}

func inspectBuildkitContainer() (dockerContainerInspect, error) {
	out, err := exec.Command("docker", "container", "inspect", buildkitContainerName).CombinedOutput()
	if err != nil {
		return dockerContainerInspect{}, fmt.Errorf("inspect Buildx container %s: %w: %s; docker/setup-buildx-action must run after runs-on/action with cleanup=false", buildkitContainerName, err, strings.TrimSpace(string(out)))
	}
	var containers []dockerContainerInspect
	if err := json.Unmarshal(out, &containers); err != nil {
		return dockerContainerInspect{}, fmt.Errorf("decode Buildx container %s inspection: %w", buildkitContainerName, err)
	}
	if len(containers) != 1 {
		return dockerContainerInspect{}, fmt.Errorf("decode Buildx container %s inspection: expected one result, got %d", buildkitContainerName, len(containers))
	}
	return containers[0], nil
}

func containerUsesBuildkitVolume(container dockerContainerInspect) bool {
	for _, mount := range container.Mounts {
		if mount.Type == "volume" && mount.Name == buildkitStateVolumeName && mount.Destination == buildkitStateTarget {
			return true
		}
	}
	return false
}

func removeDockerVolume(name string) error {
	out, err := exec.Command("docker", "volume", "rm", name).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such volume") {
		return fmt.Errorf("remove Docker volume %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
