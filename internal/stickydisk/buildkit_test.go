package stickydisk

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestSetDefaultOutputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       string
		wantImage string
	}{
		{name: "runner image pin", env: "public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.33.0-runs-on.1@sha256:abc", wantImage: "public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.33.0-runs-on.1@sha256:abc"},
		{name: "no pin", wantImage: defaultBuildkitImage},
		{name: "blank pin", env: "  ", wantImage: defaultBuildkitImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputFile := filepath.Join(t.TempDir(), "github-output")
			t.Setenv("GITHUB_OUTPUT", outputFile)
			t.Setenv(buildkitImageEnv, tc.env)
			action := githubactions.New(githubactions.WithWriter(io.Discard))

			SetDefaultOutputs(action)

			output, err := os.ReadFile(outputFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"cache-hit", "false", "buildkit-builder", buildkitBuilderName, "buildkit-image", tc.wantImage} {
				if !strings.Contains(string(output), want) {
					t.Fatalf("default outputs missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestBuildkitVersion(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  [3]int
		ok    bool
	}{
		{input: imageTag("public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.33.0-runs-on.1@sha256:0123abcd"), want: [3]int{0, 33, 0}, ok: true},
		{input: imageTag("moby/buildkit:v0.31.1"), want: [3]int{0, 31, 1}, ok: true},
		{input: imageTag("localhost:5000/buildkit:v1.2.3"), want: [3]int{1, 2, 3}, ok: true},
		{input: imageTag("public.ecr.aws/c5h5o9k1/runs-on/buildkit:buildx-stable-1")},
		{input: imageTag("localhost:5000/v9.9.9/buildkit")},
		{input: "buildkitd github.com/moby/buildkit v0.33.0 dddd5621af04ea57823085c93a063383f71d3173", want: [3]int{0, 33, 0}, ok: true},
		{input: "buildkitd github.com/moby/buildkit v0.33.0-runs-on.1 dddd5621a", want: [3]int{0, 33, 0}, ok: true},
	} {
		got, ok := buildkitVersion(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Errorf("buildkitVersion(%q) = %v, %v; want %v, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWarnIfBuildkitImageIsOlder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded string
		image    string
		warns    bool
	}{
		{name: "older pin", recorded: "buildkitd github.com/moby/buildkit v0.33.0 abc", image: "public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.32.2-runs-on.1@sha256:abc", warns: true},
		{name: "same release", recorded: "buildkitd github.com/moby/buildkit v0.33.0 abc", image: "public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.33.0-runs-on.2"},
		{name: "newer pin", recorded: "buildkitd github.com/moby/buildkit v0.32.2 abc", image: "public.ecr.aws/c5h5o9k1/runs-on/buildkit:v0.33.0-runs-on.1"},
		{name: "stable channel", recorded: "buildkitd github.com/moby/buildkit v0.33.0 abc"},
		{name: "fresh state", image: "moby/buildkit:v0.31.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateRoot := filepath.Join(t.TempDir(), "buildkit", "root")
			if err := os.MkdirAll(stateRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.recorded != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(stateRoot), buildkitVersionFile), []byte(tc.recorded), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(buildkitImageEnv, tc.image)
			var out strings.Builder
			warnIfBuildkitImageIsOlder(githubactions.New(githubactions.WithWriter(&out)), stateRoot)

			if warned := strings.Contains(out.String(), "::warning"); warned != tc.warns {
				t.Fatalf("warned = %v, want %v:\n%s", warned, tc.warns, out.String())
			}
		})
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
