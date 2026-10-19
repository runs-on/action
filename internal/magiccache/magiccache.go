package magiccache

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// URL is the address of Magic Cache's GitHub Actions cache service, as the
// job's steps see it. Custom runners name it RUNS_ON_MAGIC_CACHE_URL, and it
// comes first: whatever reads the cache must use the same service as what
// writes it. The others point ACTIONS_RESULTS_URL at Magic Cache.
func URL() string {
	if magicCacheURL := os.Getenv("RUNS_ON_MAGIC_CACHE_URL"); magicCacheURL != "" {
		return magicCacheURL
	}
	return os.Getenv("ACTIONS_RESULTS_URL")
}

// UpdateZctionsConfig sends the original GitHub backend URLs and runtime token
// to the local RunsOn cache service. The token is intentionally never logged.
func UpdateZctionsConfig(action *githubactions.Action, actionsResultsURL string, zctionsResultsURL string, zctionsCacheURL string, actionsRuntimeToken string) {
	if zctionsResultsURL == "" {
		return
	}

	configURL := actionsResultsURL + "config"
	data := url.Values{}
	// Send the ZCTIONS_RESULTS_URL value under the key 'ACTIONS_RESULTS_URL'.
	// This value is only known by the GitHub Actions runner, and is needed by the RunsOn agent cache proxy to handle artefacts caching.
	data.Set("ACTIONS_RESULTS_URL", zctionsResultsURL)
	if zctionsCacheURL != "" {
		data.Set("ACTIONS_CACHE_URL", zctionsCacheURL)
	}
	if actionsRuntimeToken != "" {
		data.Set("ACTIONS_RUNTIME_TOKEN", actionsRuntimeToken)
	}

	req, err := http.NewRequest(http.MethodPut, configURL, strings.NewReader(data.Encode()))
	if err != nil {
		action.Errorf("Failed to create config update request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		action.Errorf("Failed to send config update request: %v", err)
		return
	}
	defer resp.Body.Close()
	action.Infof("Config update response status: %s", resp.Status)
}
