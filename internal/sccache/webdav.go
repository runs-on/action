package sccache

import (
	"os"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// configureMagicCache points sccache's WebDAV backend at the agent's sccache
// cache, under the path the agent registered for the job's runtime token. The
// path is the credential: sccache needs no token, and the runtime token stays
// with the agent. The first compiler invocation starts sccache in the job's
// own environment; the action does not require or manage a sccache process.
func configureMagicCache(action *githubactions.Action, magicCache bool, runtimeToken string) {
	if !magicCache {
		action.Warningf("sccache: true needs Magic Cache; sccache is not configured.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("sccache: true needs the job's ACTIONS_RUNTIME_TOKEN; sccache is not configured.")
		return
	}
	tools, err := magiccache.Register(runtimeToken)
	if err != nil {
		action.Warningf("sccache: true could not register with the agent (%v); sccache is not configured.", err)
		return
	}
	if set := competingBackends(); len(set) > 0 {
		action.Warningf("These settings make sccache use another storage than the agent's WebDAV cache: %s. Unset them for sccache: true to take effect.", strings.Join(set, ", "))
	}
	action.AddMask(tools.ID)
	action.SetEnv("SCCACHE_GHA_ENABLED", "false")
	action.SetEnv("SCCACHE_WEBDAV_ENDPOINT", tools.URL+"/sccache")
	action.SetEnv("RUSTC_WRAPPER", "sccache")
	action.Infof("Configured sccache to use the agent's WebDAV cache. Install sccache before compiling; its first client starts the server.")
}

// competingBackends are the settings in the step's environment that select a
// storage sccache ranks above WebDAV: S3, Redis, Memcached, GCS, the GitHub
// Actions cache and Azure. sccache ignores an empty value.
func competingBackends() []string {
	var set []string
	for _, name := range []string{
		"SCCACHE_BUCKET",
		"SCCACHE_REDIS", "SCCACHE_REDIS_ENDPOINT", "SCCACHE_REDIS_CLUSTER_ENDPOINTS",
		"SCCACHE_MEMCACHED", "SCCACHE_MEMCACHED_ENDPOINT",
		"SCCACHE_GCS_BUCKET",
		"SCCACHE_GHA_VERSION",
		"SCCACHE_AZURE_BLOB_CONTAINER",
	} {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	// The action exports SCCACHE_GHA_ENABLED=false, which an env: of the job
	// or of a step can still override.
	switch strings.ToLower(os.Getenv("SCCACHE_GHA_ENABLED")) {
	case "true", "on", "1":
		set = append(set, "SCCACHE_GHA_ENABLED")
	}
	return set
}
