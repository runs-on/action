package cache

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// UpdateZctionsConfig sends the original GitHub backend URLs and runtime token
// to the local RunsOn cache service. The token is intentionally never logged.
func UpdateZctionsConfig(action *githubactions.Action, actionsResultsURL string, zctionsResultsURL string, zctionsCacheURL string, actionsRuntimeToken string) {
	if zctionsResultsURL == "" {
		return
	}

	// Older agents advertised cache API v2 as "on". BuildKit parses this
	// flag as a Go boolean and otherwise falls back to the retired v1 API.
	// Only normalize an existing affirmative flag for a RunsOn cache session;
	// absent and explicitly disabled flags must keep their original meaning.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ACTIONS_CACHE_SERVICE_V2")), "on") {
		action.SetEnv("ACTIONS_CACHE_SERVICE_V2", "true")
		action.Infof("Normalized the legacy cache API v2 flag for subsequent actions.")
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
