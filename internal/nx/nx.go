// Package nx gives Nx a remote cache in the RunsOn agent, which serves Nx's
// self-hosted remote cache API below the job's registered tool path. The path
// stands for the job's runtime token, so Nx needs no access token.
package nx

import (
	"os"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// serverEnv points Nx (20.8 or later) at a self-hosted remote cache. The job's
// own wins: a value exported to GITHUB_ENV would replace its workflow's.
const serverEnv = "NX_SELF_HOSTED_REMOTE_CACHE_SERVER"

// Configure registers the job with the agent's remote caches and exports
// NX_SELF_HOSTED_REMOTE_CACHE_SERVER for the next steps. When the job sets
// its own, or anything else is missing, it warns and exports nothing.
func Configure(action *githubactions.Action, magicCache bool, runtimeToken string) {
	if os.Getenv(serverEnv) != "" {
		action.Warningf("remote_cache: nx leaves Nx's remote cache alone: the job sets %s, which keeps pointing Nx at its own cache server. Remove it to use the RunsOn agent's cache.", serverEnv)
		return
	}
	serverURL, ok := magiccache.RegisterFor(action, "remote_cache: nx", "nx", "Nx runs without a remote cache", magicCache, runtimeToken)
	if !ok {
		return
	}
	action.SetEnv(serverEnv, serverURL)
	if os.Getenv("NX_CLOUD_ACCESS_TOKEN") != "" && os.Getenv("NX_NO_CLOUD") != "true" {
		action.Warningf("The job sets NX_CLOUD_ACCESS_TOKEN, so Nx uses Nx Cloud rather than the RunsOn agent's cache. Set NX_NO_CLOUD=true to use the agent's.")
	}
	action.Infof("Set %s: Nx uses the RunsOn agent's cache in the next steps, unless nx.json connects it to Nx Cloud (set NX_NO_CLOUD=true) or a cache plugin such as @nx/s3-cache is installed.", serverEnv)
}
