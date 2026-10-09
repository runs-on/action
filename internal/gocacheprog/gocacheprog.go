// Package gocacheprog points the go command's build cache at Magic Cache.
package gocacheprog

import (
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// Configure exports GOCACHEPROG for the job's next steps: the RunsOn agent
// serves the go command's build cache (Go 1.24+) through Magic Cache, with
// GitHub's branch scoping, when run with --gocacheprog. The agent advertises
// its path in RUNS_ON_GOCACHEPROG wherever Magic Cache runs; the helper needs
// the job's runtime token, which only actions receive.
func Configure(action *githubactions.Action, runtimeToken string) {
	agent := os.Getenv("RUNS_ON_GOCACHEPROG")
	if agent == "" {
		action.Warningf("gocacheprog needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache) and a RunsOn agent that supports it; the go command keeps its local build cache.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("gocacheprog needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; the go command keeps its local build cache.")
		return
	}
	// The go command splits GOCACHEPROG on spaces outside quotes.
	if strings.ContainsAny(agent, " \t") {
		agent = `"` + agent + `"`
	}
	command := agent + " --gocacheprog"
	action.SetEnv("GOCACHEPROG", command)
	action.SetEnv("ACTIONS_RUNTIME_TOKEN", runtimeToken)
	action.Infof("Set GOCACHEPROG=%s: the go command's build cache uses Magic Cache in the next steps.", command)
}
