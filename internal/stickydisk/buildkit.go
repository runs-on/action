package stickydisk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// Buildx derives the node, container, and state-volume names from the builder
// name; keep these aligned with its docker-container driver naming contract.
const (
	buildkitBuilderName       = "runs-on"
	buildkitNodeName          = buildkitBuilderName + "0"
	buildkitContainerName     = "buildx_buildkit_" + buildkitNodeName
	buildkitStateVolumeName   = buildkitContainerName + "_state"
	buildkitStateTarget       = "/var/lib/buildkit"
	buildkitStopWait          = 20 * time.Second
	buildkitVolumeLabelKey    = "runs-on.stickydisk"
	buildkitVolumeLabelValue  = "buildkit"
	buildkitVolumeLabel       = buildkitVolumeLabelKey + "=" + buildkitVolumeLabelValue
)

const (
	// defaultBuildkitImage is RunsOn's patched BuildKit, which runs-on/buildkit
	// moves forward to each release that passes its smoke test.
	defaultBuildkitImage = "public.ecr.aws/c5h5o9k1/runs-on/buildkit:buildx-stable-1"
	// buildkitImageEnv names the BuildKit image that RunsOn runner images pin,
	// by digest, when they are built.
	buildkitImageEnv = "RUNS_ON_BUILDKIT_IMAGE"
	// buildkitVersionFile records, next to the sticky state root, the output of
	// `buildkitd --version` for the BuildKit that last wrote that state.
	buildkitVersionFile = "buildkitd-version"
)

var buildkitVersionPattern = regexp.MustCompile(`v(\d+)\.(\d+)\.(\d+)`)

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

// SetDefaultOutputs publishes deterministic values even when sticky-disk
// caching is not requested.
func SetDefaultOutputs(action *githubactions.Action) {
	action.SetOutput("cache-hit", "false")
	action.SetOutput("buildkit-builder", buildkitBuilderName)
	action.SetOutput("buildkit-image", buildkitImage())
}

// buildkitImage returns the BuildKit image to pass to
// docker/setup-buildx-action: the one pinned by the runner image, or RunsOn's
// stable channel on runner images that don't pin one.
func buildkitImage() string {
	if image := strings.TrimSpace(os.Getenv(buildkitImageEnv)); image != "" {
		return image
	}
	return defaultBuildkitImage
}

// buildkitVersion extracts the vX.Y.Z release from `buildkitd --version`
// output or from an image reference's tag. It reports false when there is
// none, as for the buildx-stable-1 tag.
func buildkitVersion(s string) ([3]int, bool) {
	match := buildkitVersionPattern.FindStringSubmatch(s)
	if match == nil {
		return [3]int{}, false
	}
	var version [3]int
	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		version[i] = n
	}
	return version, true
}

// imageTag returns the tag of an image reference, without registry, name or
// digest.
func imageTag(image string) string {
	image, _, _ = strings.Cut(image, "@")
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < strings.LastIndex(image, "/") {
		return ""
	}
	return image[colon+1:]
}

func olderBuildkit(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func formatBuildkitVersion(v [3]int) string {
	return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2])
}

// recordedBuildkitVersion reads the version of the BuildKit that last wrote
// the sticky state next to stateRoot.
func recordedBuildkitVersion(stateRoot string) ([3]int, bool) {
	path := filepath.Join(filepath.Dir(stateRoot), buildkitVersionFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return [3]int{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return [3]int{}, false
	}
	return buildkitVersion(string(data))
}

// warnIfBuildkitImageIsOlder warns before the build when the buildkit-image
// output is older than the BuildKit that last wrote the sticky state: BuildKit
// doesn't document that an older daemon can read state from a newer one.
func warnIfBuildkitImageIsOlder(action *githubactions.Action, stateRoot string) {
	recorded, ok := recordedBuildkitVersion(stateRoot)
	if !ok {
		return
	}
	image := buildkitImage()
	version, ok := buildkitVersion(imageTag(image))
	if ok && olderBuildkit(version, recorded) {
		action.Warningf("The buildkit-image output %s (%s) is older than BuildKit %s, which last wrote this sticky BuildKit state. BuildKit doesn't document that an older version can read state written by a newer one; if builds fail, use a newer image or start a new sticky-disk lineage.", image, formatBuildkitVersion(version), formatBuildkitVersion(recorded))
	}
}

// recordBuildkitVersion records the running BuildKit version next to the
// sticky state root, and warns when it is older than the version that wrote
// the state before this job.
func recordBuildkitVersion(action *githubactions.Action, stateRoot string) {
	out, err := exec.Command("docker", "exec", buildkitContainerName, "buildkitd", "--version").CombinedOutput()
	if err != nil {
		action.Debugf("Could not read the BuildKit version of %s: %v: %s", buildkitContainerName, err, strings.TrimSpace(string(out)))
		return
	}
	line := strings.TrimSpace(string(out))
	running, ok := buildkitVersion(line)
	if !ok {
		action.Debugf("Could not parse the BuildKit version from %q", line)
		return
	}
	if recorded, ok := recordedBuildkitVersion(stateRoot); ok && olderBuildkit(running, recorded) {
		action.Warningf("This job's BuildKit %s is older than BuildKit %s, which last wrote this sticky BuildKit state. BuildKit doesn't document that an older version can read state written by a newer one; if builds fail, use a newer image or start a new sticky-disk lineage.", formatBuildkitVersion(running), formatBuildkitVersion(recorded))
	}
	path := filepath.Join(filepath.Dir(stateRoot), buildkitVersionFile)
	if err := writeFileFresh(path, []byte(line+"\n"), 0o644); err != nil {
		action.Warningf("Could not record the BuildKit version of the sticky state: %v", err)
	}
}

// setupBuildkit pre-creates the state volume expected by Buildx's single-node
// docker-container driver. This intentionally follows Buildx's
// buildx_buildkit_<node>_state naming contract; post-job verification turns an
// upstream naming change into a visible failure instead of an ephemeral cache.
// Contract source: https://github.com/docker/buildx/blob/v0.34.1/driver/docker-container/driver.go
func setupBuildkit(action *githubactions.Action, mountRoot string) (hit bool, err error) {
	stateRoot := filepath.Join(mountRoot, "buildkit", "root")
	hit = dirNonEmpty(stateRoot)
	if err := ensureRealDirectoryPath(mountRoot, stateRoot); err != nil {
		return hit, fmt.Errorf("validate BuildKit state root %s: %w", stateRoot, err)
	}

	if err := prepareBuildkitVolume(action, stateRoot); err != nil {
		return hit, err
	}
	if err := writeFileFresh(buildkitPreparedStateFile(), []byte(stateRoot), 0o600); err != nil {
		return hit, fmt.Errorf("record prepared BuildKit volume: %w", err)
	}
	warnIfBuildkitImageIsOlder(action, stateRoot)

	action.Infof("Prepared sticky BuildKit state volume '%s' for builder '%s'. Run docker/setup-buildx-action next with name=%s, driver=docker-container, and cleanup=false.", buildkitStateVolumeName, buildkitBuilderName, buildkitBuilderName)
	return hit, nil
}

func prepareBuildkitVolume(action *githubactions.Action, stateRoot string) error {
	volume, found, err := inspectDockerVolume(buildkitStateVolumeName)
	if err != nil {
		return err
	}
	if found {
		if !dockerVolumeOwnedByRunsOn(volume) {
			return fmt.Errorf("Docker volume %s already exists but is not owned by RunsOn; run runs-on/action before docker/setup-buildx-action", buildkitStateVolumeName)
		}
		if !dockerVolumeMatches(volume, stateRoot) {
			// A cancelled prior job can leave our labelled bind volume pointing
			// at that job's detached sticky disk, possibly still held by the
			// action-owned builder. Remove that builder before recreating state.
			action.Warningf("Recreating stale RunsOn BuildKit volume '%s'.", buildkitStateVolumeName)
			if err := removeBuildkitBuilder(action); err != nil {
				return fmt.Errorf("remove stale RunsOn Buildx builder: %w", err)
			}
			if err := removeDockerVolume(buildkitStateVolumeName); err != nil {
				return fmt.Errorf("remove stale RunsOn BuildKit volume: %w", err)
			}
		} else {
			// Matching bind metadata is insufficient on a reused runner: a
			// surviving container can still pin the detached prior filesystem
			// at the same path. Preserve the prepared state volume while
			// removing the action-owned builder, then verify Buildx kept it.
			if err := removeBuildkitBuilderKeepState(action); err != nil {
				return fmt.Errorf("remove surviving RunsOn Buildx builder: %w", err)
			}
			volume, found, err = inspectDockerVolume(buildkitStateVolumeName)
			if err != nil {
				return err
			}
			if !found || !dockerVolumeMatches(volume, stateRoot) {
				return fmt.Errorf("Buildx removed or changed sticky state volume %s while removing stale builder", buildkitStateVolumeName)
			}
			action.Infof("Reusing sticky BuildKit state volume '%s'.", buildkitStateVolumeName)
			return nil
		}
	}

	args := buildkitVolumeCreateArgs(stateRoot)
	if err := runLogged(action, "docker", args...); err != nil {
		return fmt.Errorf("create sticky BuildKit state volume: %w", err)
	}
	return nil
}

func removeBuildkitBuilderKeepState(action *githubactions.Action) error {
	err := runLogged(action, "docker", "buildx", "rm", "--force", "--keep-state", buildkitBuilderName)
	if err == nil {
		return nil
	}
	if !isMissingBuildxBuilderError(err) {
		return err
	}

	// Interruption between preparing the volume and setup-buildx creating its
	// metadata can leave only the known container. Remove it without -v so the
	// matching sticky state volume remains available for the next setup action.
	action.Infof("Buildx metadata for builder '%s' is absent; removing any orphaned BuildKit container while preserving its sticky volume.", buildkitBuilderName)
	return removeBuildkitContainer()
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

func dockerVolumeOwnedByRunsOn(volume dockerVolumeInspect) bool {
	return volume.Labels[buildkitVolumeLabelKey] == buildkitVolumeLabelValue
}

// cleanupBuildkit verifies that the official setup action used the prepared
// volume, then owns final shutdown because the sticky disk must not be
// snapshotted while BuildKit is still writing to it.
func cleanupBuildkit(action *githubactions.Action) error {
	stateRootBytes, err := os.ReadFile(buildkitPreparedStateFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prepared BuildKit state: %w", err)
	}
	stateRoot := strings.TrimSpace(string(stateRootBytes))
	if !filepath.IsAbs(stateRoot) {
		return fmt.Errorf("prepared BuildKit state root is not absolute: %s", stateRoot)
	}

	nodes, topologyErr := inspectBuildxNodes()
	if topologyErr == nil && len(nodes) == 0 {
		if _, containerErr := inspectBuildkitContainer(); isMissingBuildkitContainer(containerErr) {
			cleanupErr := cleanupPreparedBuildkitVolume(action, stateRoot)
			contractErr := fmt.Errorf("Buildx builder %q and container %q are absent; docker/setup-buildx-action was not run with the emitted builder name and cleanup=false", buildkitBuilderName, buildkitContainerName)
			return errors.Join(contractErr, cleanupErr)
		}
	}
	if topologyErr == nil {
		topologyErr = validateBuildkitNodes(nodes)
	}

	container, err := inspectBuildkitContainer()
	verificationErr := err
	if err == nil {
		if !containerUsesBuildkitVolume(container) {
			verificationErr = fmt.Errorf("Buildx container %s did not mount expected volume %s at %s; the pinned Buildx volume contract may have changed", buildkitContainerName, buildkitStateVolumeName, buildkitStateTarget)
		} else {
			volume, found, volumeErr := inspectDockerVolume(buildkitStateVolumeName)
			switch {
			case volumeErr != nil:
				verificationErr = volumeErr
			case !found:
				verificationErr = fmt.Errorf("BuildKit state volume %s disappeared before cleanup", buildkitStateVolumeName)
			case !dockerVolumeMatches(volume, stateRoot):
				verificationErr = fmt.Errorf("BuildKit state volume %s is not backed by %s", buildkitStateVolumeName, stateRoot)
			default:
				action.Infof("Verified sticky BuildKit state mount: %s -> %s", stateRoot, buildkitStateTarget)
				recordBuildkitVersion(action, stateRoot)
			}
		}
	}

	// Inspection failures must not skip shutdown: snapshot consistency still
	// requires best-effort removal of the builder, container, and volume.
	stopErr := runLogged(action, "docker", "stop", "--time", fmt.Sprintf("%.0f", buildkitStopWait.Seconds()), buildkitContainerName)
	builderErr := removeBuildkitBuilder(action)
	volumeErr := removeDockerVolume(buildkitStateVolumeName)

	if stopErr != nil {
		stopErr = fmt.Errorf("BuildKit did not stop cleanly before forced cleanup: %w", stopErr)
	}
	if builderErr != nil {
		builderErr = fmt.Errorf("remove BuildKit builder: %w", builderErr)
	}
	shutdownErr := errors.Join(stopErr, builderErr, volumeErr)
	var markerErr error
	// Preserve the marker unless forced builder/container and volume removal
	// succeeded, so a later post hook can retry cleanup.
	if builderErr == nil && volumeErr == nil {
		if err := os.Remove(buildkitPreparedStateFile()); err != nil && !os.IsNotExist(err) {
			markerErr = fmt.Errorf("remove prepared BuildKit state marker: %w", err)
		}
	}
	return errors.Join(topologyErr, verificationErr, shutdownErr, markerErr)
}

func cleanupPreparedBuildkitVolume(action *githubactions.Action, stateRoot string) error {
	volume, found, err := inspectDockerVolume(buildkitStateVolumeName)
	if err != nil {
		return err
	}
	if found {
		if !dockerVolumeOwnedByRunsOn(volume) {
			return fmt.Errorf("prepared BuildKit state volume %s is not owned by RunsOn", buildkitStateVolumeName)
		}
		if !dockerVolumeMatches(volume, stateRoot) {
			return fmt.Errorf("prepared BuildKit state volume %s is not backed by %s", buildkitStateVolumeName, stateRoot)
		}
		if err := removeDockerVolume(buildkitStateVolumeName); err != nil {
			return err
		}
		action.Infof("Removed prepared but unused sticky BuildKit state volume '%s'.", buildkitStateVolumeName)
	}
	if err := os.Remove(buildkitPreparedStateFile()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove prepared BuildKit state marker: %w", err)
	}
	return nil
}

func isMissingBuildkitContainer(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such container")
}

func removeBuildkitBuilder(action *githubactions.Action) error {
	builderErr := runLogged(action, "docker", "buildx", "rm", "--force", buildkitBuilderName)
	if builderErr == nil {
		return nil
	}
	if isMissingBuildxBuilderError(builderErr) {
		return removeBuildkitContainer()
	}
	action.Warningf("Buildx cleanup failed, removing its container directly: %v", builderErr)

	return errors.Join(builderErr, removeBuildkitContainer())
}

func isMissingBuildxBuilderError(err error) bool {
	notFound := fmt.Sprintf("no builder %q found", buildkitBuilderName)
	return err != nil && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(notFound))
}

func removeBuildkitContainer() error {
	out, err := exec.Command("docker", "rm", "--force", buildkitContainerName).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such container") {
		return fmt.Errorf("remove BuildKit container %s: %w: %s", buildkitContainerName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func inspectBuildxNodes() ([]string, error) {
	format := fmt.Sprintf(`{{if eq .Builder.Name %q}}{{range .Builder.Nodes}}{{println .Name}}{{end}}{{end}}`, buildkitBuilderName)
	out, err := exec.Command("docker", "buildx", "ls", "--format", format).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("inspect Buildx builder %s nodes: %w: %s", buildkitBuilderName, err, strings.TrimSpace(string(out)))
	}
	return uniqueBuildxNodes(strings.Fields(string(out))), nil
}

func uniqueBuildxNodes(nodes []string) []string {
	unique := make([]string, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if !seen[node] {
			seen[node] = true
			unique = append(unique, node)
		}
	}
	return unique
}

func validateBuildkitNodes(nodes []string) error {
	if len(nodes) != 1 || nodes[0] != buildkitNodeName {
		return fmt.Errorf("Buildx builder %s must contain exactly one node named %s; found %v (appended nodes cannot use the sticky cache)", buildkitBuilderName, buildkitNodeName, nodes)
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
