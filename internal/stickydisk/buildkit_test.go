package stickydisk

import (
	"path/filepath"
	"testing"
)

func TestBuildkitInlineConfigFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("RUNS_ON_DOCKER_HUB_MIRROR_URL", "")
		got, err := buildkitInlineConfigFromEnv()
		if err != nil || got != "" {
			t.Fatalf("config = %q, err = %v", got, err)
		}
	})

	t.Run("runner-local mirror", func(t *testing.T) {
		t.Setenv("RUNS_ON_DOCKER_HUB_MIRROR_URL", "http://10.0.1.5:6871")
		got, err := buildkitInlineConfigFromEnv()
		want := "[registry.\"docker.io\"]\n  mirrors = [\"10.0.1.5:6871\"]\n" +
			"\n[registry.\"10.0.1.5:6871\"]\n  http = true\n"
		if err != nil || got != want {
			t.Fatalf("config = %q, want %q, err = %v", got, want, err)
		}
	})

	t.Run("https mirror omits the http stanza", func(t *testing.T) {
		t.Setenv("RUNS_ON_DOCKER_HUB_MIRROR_URL", "https://mirror.example")
		got, err := buildkitInlineConfigFromEnv()
		want := "[registry.\"docker.io\"]\n  mirrors = [\"mirror.example\"]\n"
		if err != nil || got != want {
			t.Fatalf("config = %q, want %q, err = %v", got, want, err)
		}
	})

	t.Run("trailing slash", func(t *testing.T) {
		t.Setenv("RUNS_ON_DOCKER_HUB_MIRROR_URL", "http://10.0.1.5:6871/")
		got, err := buildkitInlineConfigFromEnv()
		want := "[registry.\"docker.io\"]\n  mirrors = [\"10.0.1.5:6871\"]\n" +
			"\n[registry.\"10.0.1.5:6871\"]\n  http = true\n"
		if err != nil || got != want {
			t.Fatalf("config = %q, want %q, err = %v", got, want, err)
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		t.Setenv("RUNS_ON_DOCKER_HUB_MIRROR_URL", "10.0.1.5:6871")
		if got, err := buildkitInlineConfigFromEnv(); err == nil || got != "" {
			t.Fatalf("config = %q, err = %v", got, err)
		}
	})
}

func TestNormalizeBuildkitMirrorRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{
		"",
		"mirror.example",
		"ftp://mirror.example",
		"http://user:pass@mirror.example",
		"http://mirror.example/path",
		"http://mirror.example?query=1",
		"http://mirror.example/#fragment",
		"http://mirror.example\nother.example",
	} {
		if host, _, err := normalizeBuildkitMirror(value); err == nil {
			t.Errorf("normalizeBuildkitMirror(%q) = %q, expected error", value, host)
		}
	}
}

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
