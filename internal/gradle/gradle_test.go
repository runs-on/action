package gradle

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureWarnsAndLeavesGradleAlone(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		magicCache bool
		token      string
		want       string
	}{
		{name: "unknown mode", mode: "push", magicCache: true, token: "t", want: `"push"`},
		{name: "no Magic Cache", mode: "true", token: "t", want: "needs Magic Cache"},
		{name: "no runtime token", mode: "true", magicCache: true, want: "ACTIONS_RUNTIME_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GRADLE_USER_HOME", t.TempDir())
			t.Setenv("RUNNER_TEMP", t.TempDir())
			var log bytes.Buffer
			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, tc.magicCache, tc.token)
			if !strings.Contains(log.String(), "::warning::") || !strings.Contains(log.String(), tc.want) {
				t.Errorf("log %q, want a warning containing %q", log.String(), tc.want)
			}
			if _, err := os.Stat(initScriptPath()); !os.IsNotExist(err) {
				t.Errorf("the init script was written: %v", err)
			}
		})
	}
}

func TestServerEnvLeavesOutSecrets(t *testing.T) {
	got := serverEnv([]string{"PATH=/bin", "INPUT_TOKEN=ghs_x", "ACTIONS_RUNTIME_TOKEN=jwt", "ACTIONS_RESULTS_URL=http://x", "GITHUB_TOKEN=ghs_y", "RUNNER_TEMP=/tmp/r"})
	if want := []string{"PATH=/bin", "RUNNER_TEMP=/tmp/r"}; !slices.Equal(got, want) {
		t.Errorf("serverEnv = %q, want %q", got, want)
	}
}
