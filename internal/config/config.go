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
	Cache               []string
	CachePaths          []string
	CacheWaitTimeout    time.Duration
	CacheFailOnMissing  bool
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

	cacheInput := action.GetInput("cache")
	if cacheInput != "" {
		cfg.Cache = splitList(cacheInput)
	}

	pathInput := action.GetInput("path")
	if pathInput != "" {
		for _, entry := range strings.Split(pathInput, "\n") {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				cfg.CachePaths = append(cfg.CachePaths, entry)
			}
		}
	}

	cfg.CacheWaitTimeout = 5 * time.Minute
	waitTimeoutStr := action.GetInput("wait_timeout")
	if waitTimeoutStr != "" {
		if timeout, err := time.ParseDuration(waitTimeoutStr); err == nil {
			cfg.CacheWaitTimeout = timeout
		} else {
			action.Warningf("Error parsing 'wait_timeout' input '%s': %v. Using default 5m.", waitTimeoutStr, err)
		}
	}

	failOnMissingStr := action.GetInput("fail_on_missing")
	if failOnMissingStr != "" {
		var err error
		cfg.CacheFailOnMissing, err = strconv.ParseBool(failOnMissingStr)
		if err != nil {
			action.Warningf("Error parsing 'fail_on_missing' input '%s': %v. Assuming false.", failOnMissingStr, err)
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
	action.Infof("Input 'cache': %v", cfg.Cache)
	action.Infof("Input 'path': %v", cfg.CachePaths)

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

func (c *Config) HasShowCosts() bool {
	return c.ShowCosts != "inline"
}

func (c *Config) HasMetrics() bool {
	return c.IsUsingRunsOn() && c.IsUsingLinux() && len(c.Metrics) > 0
}

func (c *Config) HasSccache() bool {
	return c.IsUsingRunsOn() && c.IsUsingLinux() && c.Sccache != ""
}

// HasStickyDiskCache reports whether sticky disk caching was requested. The
// Linux check happens inside the stickydisk package so non-Linux runners get
// an explicit warning instead of a silent skip.
func (c *Config) HasStickyDiskCache() bool {
	return c.IsUsingRunsOn() && (len(c.Cache) > 0 || len(c.CachePaths) > 0)
}

// splitList splits a newline and/or comma separated input into trimmed,
// non-empty entries.
func splitList(input string) []string {
	var entries []string
	for _, entry := range strings.FieldsFunc(input, func(r rune) bool { return r == '\n' || r == ',' }) {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (c *Config) IsUsingRunsOn() bool {
	return os.Getenv("RUNS_ON_RUNNER_NAME") != ""
}

func (c *Config) IsUsingLinux() bool {
	return runtime.GOOS == "linux"
}
