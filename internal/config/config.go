package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// Config holds the action's configuration values derived from inputs and environment.
type Config struct {
	ShowEnv             bool
	ShowCosts           string
	Metrics             []string
	NetworkInterface    string
	DiskDevice          string
	Sccache             string
	StickyCache         []string
	StickyWaitTimeout   time.Duration
	ZctionsResultsURL   string
	ZctionsCacheURL     string
	ActionsResultsURL   string
	ActionsRuntimeToken string
}

type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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

	metricsInput := action.GetInput("metrics")
	if metricsInput != "" {
		cfg.Metrics = strings.Split(strings.ReplaceAll(metricsInput, " ", ""), ",")
	}

	cfg.NetworkInterface = action.GetInput("network_interface")
	if cfg.NetworkInterface == "" {
		cfg.NetworkInterface = "auto"
	}

	cfg.DiskDevice = action.GetInput("disk_device")
	if cfg.DiskDevice == "" {
		cfg.DiskDevice = "auto"
	}

	cfg.Sccache = action.GetInput("sccache")

	stickyCacheInput := action.GetInput("sticky_cache")
	if stickyCacheInput != "" {
		for _, entry := range strings.Split(stickyCacheInput, "\n") {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				cfg.StickyCache = append(cfg.StickyCache, entry)
			}
		}
	}

	cfg.StickyWaitTimeout = 5 * time.Minute
	waitTimeoutStr := action.GetInput("sticky_wait_timeout")
	if waitTimeoutStr != "" {
		if timeout, err := time.ParseDuration(waitTimeoutStr); err == nil {
			cfg.StickyWaitTimeout = timeout
		} else {
			action.Warningf("Error parsing 'sticky_wait_timeout' input '%s': %v. Using default 5m.", waitTimeoutStr, err)
		}
	}

	cfg.ZctionsResultsURL = os.Getenv("ZCTIONS_RESULTS_URL")
	cfg.ZctionsCacheURL = os.Getenv("ZCTIONS_CACHE_URL")
	cfg.ActionsResultsURL = os.Getenv("ACTIONS_RESULTS_URL")
	cfg.ActionsRuntimeToken = os.Getenv("ACTIONS_RUNTIME_TOKEN")

	action.Infof("Input 'show_env': %t", cfg.ShowEnv)
	action.Infof("Input 'show_costs': %s", cfg.ShowCosts)
	action.Infof("Input 'metrics': %v", cfg.Metrics)
	action.Infof("Input 'network_interface': %s", cfg.NetworkInterface)
	action.Infof("Input 'disk_device': %s", cfg.DiskDevice)
	action.Infof("Input 'sccache': %s", cfg.Sccache)
	action.Infof("Input 'sticky_cache': %v", cfg.StickyCache)
	action.Infof("Input 'sticky_wait_timeout': %s", cfg.StickyWaitTimeout)

	if cfg.ZctionsResultsURL != "" {
		action.Infof("ZCTIONS_RESULTS_URL is set: %s", cfg.ZctionsResultsURL)
	} else {
		action.Infof("ZCTIONS_RESULTS_URL is not set.")
	}
	if cfg.ZctionsCacheURL != "" {
		action.Infof("ZCTIONS_CACHE_URL is set: %s", cfg.ZctionsCacheURL)
	} else {
		action.Infof("ZCTIONS_CACHE_URL is not set.")
	}
	if cfg.ActionsRuntimeToken != "" {
		action.Infof("ACTIONS_RUNTIME_TOKEN is set.")
	} else {
		action.Infof("ACTIONS_RUNTIME_TOKEN is not set.")
	}

	return cfg, nil
}

func (c *Config) HasShowEnv() bool {
	return c.ShowEnv
}

func (c *Config) HasMetrics() bool {
	return c.IsUsingRunsOn() && c.IsUsingLinux() && len(c.Metrics) > 0
}

func (c *Config) HasSccache() bool {
	return c.IsUsingRunsOn() && c.IsUsingLinux() && c.Sccache != ""
}

// HasStickyDiskCache reports whether sticky disk caching was requested.
// Platform and RunsOn availability checks happen inside the stickydisk package
// so an explicit persistence request never degrades into a silent no-op.
func (c *Config) HasStickyDiskCache() bool {
	return len(c.StickyCache) > 0
}

func (c *Config) IsUsingRunsOn() bool {
	return os.Getenv("RUNS_ON_RUNNER_NAME") != ""
}

func (c *Config) IsUsingLinux() bool {
	return runtime.GOOS == "linux"
}
