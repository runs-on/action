package stickydisk

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildkitNamesMatchPinnedBuildxContract(t *testing.T) {
	if buildkitBuilderName != "runs-on" {
		t.Fatalf("builder = %q", buildkitBuilderName)
	}
	if buildkitNodeName != "runs-on0" {
		t.Fatalf("node = %q", buildkitNodeName)
	}
	if buildkitContainerName != "buildx_buildkit_runs-on0" {
		t.Fatalf("container = %q", buildkitContainerName)
	}
	if buildkitStateVolumeName != "buildx_buildkit_runs-on0_state" {
		t.Fatalf("volume = %q", buildkitStateVolumeName)
	}
}

func TestBuildkitInlineConfigFromEnv(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE", "123456789012.dkr.ecr.us-east-1.amazonaws.com")
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE_DOCKER_HUB_MIRROR", "false")
		got, err := buildkitInlineConfigFromEnv()
		if err != nil || got != "" {
			t.Fatalf("config = %q, err = %v", got, err)
		}
	})

	t.Run("hostname", func(t *testing.T) {
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE", "123456789012.dkr.ecr.us-east-1.amazonaws.com")
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE_DOCKER_HUB_MIRROR", "true")
		got, err := buildkitInlineConfigFromEnv()
		want := "[registry.\"docker.io\"]\n  mirrors = [\"123456789012.dkr.ecr.us-east-1.amazonaws.com\"]\n"
		if err != nil || got != want {
			t.Fatalf("config = %q, want %q, err = %v", got, want, err)
		}
	})

	t.Run("https URL", func(t *testing.T) {
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE", "https://123456789012.dkr.ecr.us-east-1.amazonaws.com/")
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE_DOCKER_HUB_MIRROR", "TRUE")
		got, err := buildkitInlineConfigFromEnv()
		want := "[registry.\"docker.io\"]\n  mirrors = [\"123456789012.dkr.ecr.us-east-1.amazonaws.com\"]\n"
		if err != nil || got != want {
			t.Fatalf("config = %q, want %q, err = %v", got, want, err)
		}
	})

	t.Run("missing registry", func(t *testing.T) {
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE", "")
		t.Setenv("RUNS_ON_ECR_PULL_THROUGH_CACHE_DOCKER_HUB_MIRROR", "true")
		if got, err := buildkitInlineConfigFromEnv(); err == nil || got != "" {
			t.Fatalf("config = %q, err = %v", got, err)
		}
	})
}

func TestNormalizeBuildkitMirrorRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{
		"http://mirror.example",
		"https://user:pass@mirror.example",
		"https://mirror.example/path",
		"https://mirror.example?query=1",
		"https://mirror.example/#fragment",
		"mirror.example\nother.example",
	} {
		if got, err := normalizeBuildkitMirror(value); err == nil {
			t.Errorf("normalizeBuildkitMirror(%q) = %q, expected error", value, got)
		}
	}
}

func TestBuildkitVolumeCreateArgs(t *testing.T) {
	stateRoot := "/mnt/runs-on/stickydisk/buildkit/root"
	want := []string{
		"volume", "create",
		"--driver", "local",
		"--label", "runs-on.stickydisk=buildkit",
		"--opt", "type=none",
		"--opt", "o=bind",
		"--opt", "device=" + stateRoot,
		"buildx_buildkit_runs-on0_state",
	}
	if got := buildkitVolumeCreateArgs(stateRoot); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
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
