package gocacheprog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureExportsGOCACHEPROGWhereTheAgentServesIt(t *testing.T) {
	for _, tc := range []struct {
		name, advertised, token string
		want                    []string
	}{
		{name: "agent serves it", advertised: "/runs-on/agent --gocacheprog", token: "job-token", want: []string{"GOCACHEPROG", "/runs-on/agent --gocacheprog", "ACTIONS_RUNTIME_TOKEN", "job-token"}},
		{name: "no Magic Cache or older agent", token: "job-token"},
		{name: "no runtime token", advertised: "/runs-on/agent --gocacheprog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNS_ON_GOCACHEPROG", tc.advertised)
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.token)

			exported, _ := os.ReadFile(envFile)
			for _, want := range tc.want {
				if !strings.Contains(string(exported), want) {
					t.Errorf("exported env %q lacks %q", exported, want)
				}
			}
			if len(tc.want) == 0 && len(exported) > 0 {
				t.Errorf("exported %q, want nothing", exported)
			}
			if strings.Contains(log.String(), "job-token") {
				t.Errorf("the runtime token reached the log")
			}
		})
	}
}
