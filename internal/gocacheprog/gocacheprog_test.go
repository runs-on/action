package gocacheprog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestConfigureExportsGOCACHEPROGOnlyWithMagicCacheAndAToken(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	started := startPrefetch
	defer func() { startPrefetch = started }()
	for _, tc := range []struct {
		name       string
		mode       string
		magicCache bool
		token      string
		want       []string
		wantShared bool
		// wantPrefetch is whether each prefetch started shares test results.
		wantPrefetch []bool
	}{
		{name: "builds only", mode: "true", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag, "ACTIONS_RUNTIME_TOKEN", "job-token"}, wantPrefetch: []bool{false}},
		{name: "with test results", mode: "with-tests", magicCache: true, token: "job-token", want: []string{"GOCACHEPROG", self, Flag}, wantShared: true, wantPrefetch: []bool{true}},
		{name: "unknown mode", mode: "tests", magicCache: true, token: "job-token"},
		{name: "no Magic Cache", mode: "true", token: "job-token"},
		{name: "no runtime token", mode: "true", magicCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), "env")
			t.Setenv("GITHUB_ENV", envFile)
			var log bytes.Buffer
			var prefetches []bool
			startPrefetch = func(_ string, shareTestResults bool) error {
				prefetches = append(prefetches, shareTestResults)
				return nil
			}

			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, tc.magicCache, tc.token)

			if !slices.Equal(prefetches, tc.wantPrefetch) {
				t.Errorf("prefetches started: %v, want %v", prefetches, tc.wantPrefetch)
			}

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

// runtimeToken is a token as GitHub signs them, reduced to the claim that
// names the job: "<run>.<job>.<matrix leg>".
func runtimeToken(orchID string) string {
	payload, _ := json.Marshal(map[string]string{"orch_id": orchID})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestListKeyIsTheJobAcrossRuns(t *testing.T) {
	t.Setenv("GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/ci.yml@refs/heads/main")
	t.Setenv("GITHUB_JOB", "build")
	t.Setenv("RUNNER_OS", "Linux")
	t.Setenv("RUNNER_ARCH", "X64")
	leg := ListKey(runtimeToken("run-1.build.ubuntu-latest_1_26"))

	for _, tc := range []struct {
		name  string
		token string
		env   [2]string
		same  bool
	}{
		{name: "the same job in another run", token: runtimeToken("run-2.build.ubuntu-latest_1_26"), same: true},
		{name: "the same job on another branch", token: runtimeToken("run-2.build.ubuntu-latest_1_26"), env: [2]string{"GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/ci.yml@refs/pull/7/merge"}, same: true},
		{name: "another leg of the matrix", token: runtimeToken("run-1.build.ubuntu-latest_1_25")},
		{name: "another job", token: runtimeToken("run-1.test.ubuntu-latest_1_26")},
		{name: "the same job name in another workflow", token: runtimeToken("run-1.build.ubuntu-latest_1_26"), env: [2]string{"GITHUB_WORKFLOW_REF", "acme/app/.github/workflows/release.yml@refs/heads/main"}},
		{name: "another architecture", token: runtimeToken("run-1.build.ubuntu-latest_1_26"), env: [2]string{"RUNNER_ARCH", "ARM64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env[0] != "" {
				t.Setenv(tc.env[0], tc.env[1])
			}
			if got := ListKey(tc.token); (got == leg) != tc.same {
				t.Errorf("list key %q, same as the first job's %q: %v, want %v", got, leg, got == leg, tc.same)
			}
		})
	}

	// A token that does not name the job falls back to the job's key in its
	// workflow, where the legs of a matrix are one job.
	if a, b := ListKey("not-a-jwt"), ListKey(runtimeToken("")); a != b || a == leg {
		t.Errorf("list keys without a job claim: %q and %q, want the same one, apart from %q", a, b, leg)
	}
}
