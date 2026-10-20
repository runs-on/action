package sccache

import (
	"os"
	"slices"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// configureMagicCache points sccache's WebDAV backend at the agent's sccache
// cache, at the URL the agent registered for the job's runtime token. The
// URL is the credential: sccache needs no token, and the runtime token stays
// with the agent. The first compiler invocation starts sccache in the job's
// own environment; the action does not require or manage a sccache process.
func configureMagicCache(action *githubactions.Action, magicCache bool, runtimeToken string) {
	// sccache sends these to its WebDAV endpoint: once the action replaces the
	// job's endpoint, to the agent, over plain HTTP.
	if set := setVariables("SCCACHE_WEBDAV_TOKEN", "SCCACHE_WEBDAV_USERNAME", "SCCACHE_WEBDAV_PASSWORD"); len(set) > 0 {
		action.Warningf("These settings hold credentials for another WebDAV server, which sccache would send to the agent's cache: %s. Unset them for sccache: true to take effect; sccache is not configured.", strings.Join(set, ", "))
		return
	}
	endpoint, ok := magiccache.RegisterFor(action, "sccache: true", "sccache", "sccache is not configured", magicCache, runtimeToken)
	if !ok {
		return
	}
	if set := competingBackends(); len(set) > 0 {
		action.Warningf("These settings make sccache use another storage than the agent's WebDAV cache: %s. Unset them for sccache: true to take effect.", strings.Join(set, ", "))
	}
	action.SetEnv("SCCACHE_GHA_ENABLED", "false")
	action.SetEnv("SCCACHE_WEBDAV_ENDPOINT", endpoint)
	action.SetEnv("RUSTC_WRAPPER", "sccache")
	action.Infof("Configured sccache to use the agent's WebDAV cache. Install sccache before compiling; its first client starts the server.")
}

// setVariables are the names among names that the step's environment sets
// to a non-empty value, which sccache ignores.
func setVariables(names ...string) []string {
	var set []string
	for _, name := range names {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	return set
}

// competingBackends are the settings in the step's environment that select a
// storage sccache ranks above WebDAV: S3, Redis, Memcached, GCS, the GitHub
// Actions cache and Azure, or a multi-level chain without WebDAV.
func competingBackends() []string {
	set := setVariables(
		"SCCACHE_BUCKET",
		"SCCACHE_REDIS", "SCCACHE_REDIS_ENDPOINT", "SCCACHE_REDIS_CLUSTER_ENDPOINTS",
		"SCCACHE_MEMCACHED", "SCCACHE_MEMCACHED_ENDPOINT",
		"SCCACHE_GCS_BUCKET",
		"SCCACHE_GHA_VERSION",
		"SCCACHE_AZURE_BLOB_CONTAINER",
	)
	// An explicit chain names every storage sccache uses.
	if chain := os.Getenv("SCCACHE_MULTILEVEL_CHAIN"); chain != "" && !slices.ContainsFunc(strings.Split(chain, ","), func(level string) bool {
		return strings.EqualFold(strings.TrimSpace(level), "webdav")
	}) {
		set = append(set, "SCCACHE_MULTILEVEL_CHAIN")
	}
	// The action exports SCCACHE_GHA_ENABLED=false, which an env: of the job
	// or of a step can still override.
	switch strings.ToLower(os.Getenv("SCCACHE_GHA_ENABLED")) {
	case "true", "on", "1":
		set = append(set, "SCCACHE_GHA_ENABLED")
	}
	return set
}
