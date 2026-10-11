package gocacheprog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureExportsGOCACHEPROGOnlyWithMagicCacheAndAToken(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		mode       string
		magicCache bool
		token      string
		want       []string
		wantShared bool
	}{
		{name: "builds only", mode: "true", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag, "ACTIONS_RUNTIME_TOKEN", "job-token"}},
		{name: "with test results", mode: "with-tests", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag}, wantShared: true},
		{name: "unknown mode", mode: "tests", magicCache: true, token: "job-token"},
		{name: "no Magic Cache", mode: "true", token: "job-token"},
		{name: "no runtime token", mode: "true", magicCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			var log bytes.Buffer

			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, tc.magicCache, tc.token)

			exported, _ := os.ReadFile(envFile)
			for _, want := range tc.want {
				if !strings.Contains(string(exported), want) {
					t.Errorf("exported env %q lacks %q", exported, want)
				}
			}
			if shared := strings.Contains(string(exported), ShareTestResultsFlag); shared != tc.wantShared {
				t.Errorf("exported env shares test results: %v, want %v", shared, tc.wantShared)
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
