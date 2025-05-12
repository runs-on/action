package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// Config holds the action's configuration values derived from inputs and environment.
type Config struct {
	ShowEnv           bool
	ShowCosts         string
	ZctionsResultsURL string
	SnapshotDirs      []string
	SnapshotVersion   string
}

// NewConfigFromInputs parses action inputs and environment variables to build the Config struct.
func NewConfigFromInputs(action *githubactions.Action) (*Config, error) {
	cfg := &Config{}

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

	dirs := strings.Split(action.GetInput("snapshot_dirs"), "\n")
	action.Infof("dirs: %v", dirs)
	cfg.SnapshotDirs = make([]string, 0)
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if strings.HasPrefix(dir, "/") {
			cfg.SnapshotDirs = append(cfg.SnapshotDirs, dir)
		} else {
			action.Warningf("Skipping snapshot_dir '%s' because it does not start with '/'.", dir)
		}
	}

	cfg.SnapshotVersion = action.GetInput("snapshot_version")
	if cfg.SnapshotVersion == "" {
		cfg.SnapshotVersion = "v1"
	}

	cfg.ZctionsResultsURL = os.Getenv("ZCTIONS_RESULTS_URL")

	action.Infof("Input 'show_env': %t", cfg.ShowEnv)
	action.Infof("Input 'show_costs': %s", cfg.ShowCosts)
	action.Infof("Input 'snapshot_dirs': %v", cfg.SnapshotDirs)
	action.Infof("Input 'snapshot_version': %s", cfg.SnapshotVersion)
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
