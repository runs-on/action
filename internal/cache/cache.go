package cache

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/runs-on/action/internal/config"
	"github.com/sethvargo/go-githubactions"
)

// UpdateZctionsConfig sends a PUT request if ZCTIONS_RESULTS_URL is set.
func UpdateZctionsConfig(action *githubactions.Action, cfg *config.Config) {
	if cfg.ZctionsResultsURL == "" {
		return
	}

	configURL := cfg.ZctionsResultsURL + "/config" // Simplified string concatenation
	data := url.Values{}
	// Send the ZCTIONS_RESULTS_URL value under the key 'ACTIONS_RESULTS_URL'
	data.Set("ACTIONS_RESULTS_URL", cfg.ZctionsResultsURL)

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
