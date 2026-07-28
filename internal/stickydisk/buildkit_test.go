package stickydisk

import (
	"path/filepath"
	"testing"
)

func TestDockerVolumeMatches(t *testing.T) {
	stateRoot := filepath.Clean("/mnt/runs-on/stickydisk/buildkit/root")
	matching := dockerVolumeInspect{
		Driver: "local",
		Options: map[string]string{
			"type":   "none",
			"o":      "bind",
			"device": stateRoot,
		},
	}
	if !dockerVolumeMatches(matching, stateRoot) {
		t.Fatal("expected matching volume")
	}
	matching.Options["device"] = "/tmp/not-sticky"
	if dockerVolumeMatches(matching, stateRoot) {
		t.Fatal("expected mismatched device to be rejected")
	}
}

func TestDockerVolumeOwnedByRunsOn(t *testing.T) {
	volume := dockerVolumeInspect{Labels: map[string]string{buildkitVolumeLabelKey: buildkitVolumeLabelValue}}
	if !dockerVolumeOwnedByRunsOn(volume) {
		t.Fatal("expected RunsOn-labelled volume to be owned")
	}
	volume.Labels[buildkitVolumeLabelKey] = "other"
	if dockerVolumeOwnedByRunsOn(volume) {
		t.Fatal("expected foreign volume to be rejected")
	}
}

func TestValidateBuildkitNodes(t *testing.T) {
	if err := validateBuildkitNodes([]string{buildkitNodeName}); err != nil {
		t.Fatalf("single sticky node failed validation: %v", err)
	}
	for _, nodes := range [][]string{
		nil,
		{"other"},
		{buildkitNodeName, "runs-on1"},
	} {
		if err := validateBuildkitNodes(nodes); err == nil {
			t.Errorf("nodes %v passed validation", nodes)
		}
	}
}

func TestContainerUsesBuildkitVolume(t *testing.T) {
	container := dockerContainerInspect{Mounts: []dockerMount{{
		Type:        "volume",
		Name:        buildkitStateVolumeName,
		Destination: buildkitStateTarget,
	}}}
	if !containerUsesBuildkitVolume(container) {
		t.Fatal("expected BuildKit state mount")
	}
	container.Mounts[0].Name = "ephemeral"
	if containerUsesBuildkitVolume(container) {
		t.Fatal("expected unexpected volume to be rejected")
	}
}
