package gocacheprog

import (
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// Flag makes this action's binary serve the go command's build cache.
// ShareTestResultsFlag makes it share test results between jobs too.
const (
	Flag                 = "--gocacheprog"
	ShareTestResultsFlag = "--share-test-results"
)

// The values of the gocacheprog input.
const (
	modeBuilds    = "true"
	modeWithTests = "with-tests"
)

// Configure exports GOCACHEPROG for the job's next steps: this binary, run
// with Flag, serves the go command's build cache (Go 1.24+) through Magic
// Cache. The helper needs the job's runtime token, which only actions
// receive. Without Magic Cache or a token, the go command keeps its local
// build cache.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	if mode != modeBuilds && mode != modeWithTests {
		action.Warningf("gocacheprog is %q, want %q or %q; the go command keeps its local build cache.", mode, modeBuilds, modeWithTests)
		return
	}
	if !magicCache {
		action.Warningf("gocacheprog needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); the go command keeps its local build cache.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("gocacheprog needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; the go command keeps its local build cache.")
		return
	}
	helper, err := os.Executable()
	if err != nil {
		action.Warningf("gocacheprog could not locate the action's binary (%v); the go command keeps its local build cache.", err)
		return
	}
	// The go command splits GOCACHEPROG on spaces outside quotes.
	if strings.ContainsAny(helper, " \t") {
		helper = `"` + helper + `"`
	}
	command := helper + " " + Flag
	if mode == modeWithTests {
		command += " " + ShareTestResultsFlag
	}
	action.SetEnv("GOCACHEPROG", command)
	action.SetEnv("ACTIONS_RUNTIME_TOKEN", runtimeToken)
	action.Infof("Set GOCACHEPROG=%s: the go command's build cache uses Magic Cache in the next steps.", command)
}
