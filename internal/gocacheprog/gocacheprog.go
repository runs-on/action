package gocacheprog

import (
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// Flag makes this action's binary serve the go command's build cache.
const Flag = "--gocacheprog"

// Configure exports GOCACHEPROG for the job's next steps: this binary, run
// with Flag, serves the go command's build cache (Go 1.24+) through Magic
// Cache. The helper needs the job's runtime token, which only actions
// receive. Without Magic Cache or a token, the go command keeps its local
// build cache.
func Configure(action *githubactions.Action, magicCache bool, runtimeToken string) {
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
	action.SetEnv("GOCACHEPROG", helper+" "+Flag)
	action.SetEnv("ACTIONS_RUNTIME_TOKEN", runtimeToken)
	action.Infof("Set GOCACHEPROG=%s %s: the go command's build cache uses Magic Cache in the next steps.", helper, Flag)
}
