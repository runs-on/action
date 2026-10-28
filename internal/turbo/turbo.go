// Package turbo gives Turborepo a remote cache in the RunsOn agent, which
// serves Turborepo's remote cache API below the job's registered tool path.
// turbo enables a remote cache when it has a token and a team, and sends both
// on every request: the path stands for the job's runtime token, so the
// action exports placeholders the agent ignores.
package turbo

import (
	"os"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// settings are the variables that point turbo at a remote cache. The job's own
// win: values exported to GITHUB_ENV would replace those of its workflow.
var settings = []string{"TURBO_API", "TURBO_TOKEN", "TURBO_TEAM", "TURBO_TEAMID"}

// placeholder is the token and team turbo needs to enable its remote cache.
// It is no secret: the agent authorizes requests by their path.
const placeholder = "runs-on"

// readOnlyCache keeps turbo's local cache and reads the remote one without
// uploading: after one refused upload, turbo stops using the remote cache for
// the rest of its run, reads included.
const readOnlyCache = "local:rw,remote:r"

// Configure registers the job with the agent's remote caches and exports
// TURBO_API, TURBO_TOKEN and TURBO_TEAM for the next steps, and TURBO_CACHE
// when the job's token cannot write, as in a pull request from a fork. When
// the job sets its own remote cache, or anything else is missing, it warns
// and exports nothing.
func Configure(action *githubactions.Action, magicCache bool, runtimeToken string) {
	var set []string
	for _, name := range settings {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) > 0 {
		action.Warningf("remote_cache: turbo leaves turbo's remote cache alone: the job sets %s, which keep pointing turbo at its own remote cache. Remove them to use the RunsOn agent's cache.", strings.Join(set, ", "))
		return
	}
	apiURL, ok := magiccache.RegisterFor(action, "remote_cache: turbo", "turbo", "turbo builds without a remote cache", magicCache, runtimeToken)
	if !ok {
		return
	}
	action.SetEnv("TURBO_API", apiURL)
	action.SetEnv("TURBO_TOKEN", placeholder)
	action.SetEnv("TURBO_TEAM", placeholder)
	if !magiccache.Writable(runtimeToken) {
		if cache := os.Getenv("TURBO_CACHE"); cache == "" {
			action.SetEnv("TURBO_CACHE", readOnlyCache)
			action.Infof("This job's token cannot write to the cache, as in a pull request from a fork: set TURBO_CACHE=%s, so turbo reads the RunsOn agent's cache without uploading.", readOnlyCache)
		} else {
			action.Infof("This job's token cannot write to the cache, and the job sets TURBO_CACHE=%s: if it lets turbo upload, turbo stops using the remote cache after its first upload.", cache)
		}
	}
	action.Infof("Set TURBO_API: turbo uses the RunsOn agent's cache in the next steps.")
}
