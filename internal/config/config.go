package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// Config holds the action's configuration values derived from inputs and environment.
type Config struct {
	ShowEnv           bool
	ShowCosts         string
	Metrics           []string
	ZctionsResultsURL string
	ActionsResultsURL string
	RunnerConfig      *RunnerConfig
}

type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type RunnerConfig struct {
	DefaultBranch string `json:"defaultBranch"`
	CustomTags    []Tag  `json:"customTags"`
}

// NewConfigFromInputs parses action inputs and environment variables to build the Config struct.
func NewConfigFromInputs(action *githubactions.Action) (*Config, error) {
	cfg := &Config{}

	configBytes, err := os.ReadFile(filepath.Join(os.Getenv("RUNS_ON_HOME"), "config.json"))
	if err != nil {
		action.Warningf("Error reading RunsOn config file: %v", err)
		return cfg, nil
	} else {
		var runnerConfig RunnerConfig
		if err := json.Unmarshal(configBytes, &runnerConfig); err != nil {
			action.Warningf("Error parsing RunsOn config file: %v", err)
		} else {
			cfg.RunnerConfig = &runnerConfig
			action.Infof("Runner config: %v", cfg.RunnerConfig)
		}
	}

	showEnvStr := action.GetInput("show_env")
	if showEnvStr != "" {
		var err error
		cfg.ShowEnv, err = strconv.ParseBool(showEnvStr)
		if err != nil {
			action.Warningf("Error parsing 'show_env' input '%s': %v. Assuming false.", showEnvStr, err)
		}
	}

	cfg.ShowCosts = action.GetInput("show_costs")
	if cfg.ShowCosts == "" {
		cfg.ShowCosts = "inline"
	}

	metricsInput := action.GetInput("metrics")
	if metricsInput != "" {
		cfg.Metrics = strings.Split(strings.ReplaceAll(metricsInput, " ", ""), ",")
	}

	cfg.ZctionsResultsURL = os.Getenv("ZCTIONS_RESULTS_URL")
	cfg.ActionsResultsURL = os.Getenv("ACTIONS_RESULTS_URL")

	action.Infof("Input 'show_env': %t", cfg.ShowEnv)
	action.Infof("Input 'show_costs': %s", cfg.ShowCosts)
	action.Infof("Input 'metrics': %v", cfg.Metrics)

	if cfg.ZctionsResultsURL != "" {
		action.Infof("ZCTIONS_RESULTS_URL is set: %s", cfg.ZctionsResultsURL)
	} else {
		action.Infof("ZCTIONS_RESULTS_URL is not set.")
	}

	return cfg, nil
}

func (c *Config) HasShowEnv() bool {
	return c.ShowEnv
}

func (c *Config) HasShowCosts() bool {
	return c.ShowCosts != "inline"
}

func (c *Config) HasMetrics() bool {
	return len(c.Metrics) > 0
}
