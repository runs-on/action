package cache

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// UpdateZctionsConfig sends a PUT request if ZCTIONS_RESULTS_URL is set.
func UpdateZctionsConfig(action *githubactions.Action, actionsResultsURL string, zctionsResultsURL string) {
	if zctionsResultsURL == "" {
		return
	}

	configURL := actionsResultsURL + "config"
	data := url.Values{}
	// Send the ZCTIONS_RESULTS_URL value under the key 'ACTIONS_RESULTS_URL'.
	// This value is only known by the GitHub Actions runner, and is needed by the RunsOn agent cache proxy to handle artefacts caching.
	data.Set("ACTIONS_RESULTS_URL", zctionsResultsURL)

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
