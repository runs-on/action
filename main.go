package main

import (
	"context"
	"flag"

	"github.com/runs-on/action/internal/cache"
	"github.com/runs-on/action/internal/config"
	"github.com/runs-on/action/internal/costs"
	"github.com/runs-on/action/internal/env"
	"github.com/runs-on/action/internal/monitoring"
	"github.com/runs-on/action/internal/sccache"
	"github.com/sethvargo/go-githubactions"
)

// handleMainExecution contains the original main logic.
func handleMainExecution(action *githubactions.Action, ctx context.Context) {
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Fatalf("Failed to load configuration: %v", err)
	}

	// Skip all operations if not running on RunsOn runners
	if !cfg.IsUsingRunsOn() {
		action.Infof("Not running on RunsOn runner, skipping all operations")
		return
	}

	// Execute logic based on configuration
	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	cache.UpdateZctionsConfig(action, cfg.ActionsResultsURL, cfg.ZctionsResultsURL)

	if cfg.HasShowCosts() {
		action.Infof("show_costs is enabled. You will find cost details in the post-execution step of this action.")
	}

	// Configure sccache if requested
	if cfg.HasSccache() {
		if err := sccache.ConfigureSccache(action, cfg.Sccache); err != nil {
			action.Errorf("Failed to configure sccache: %v", err)
		}
	}

	// Configure CloudWatch metrics if requested
	if cfg.HasMetrics() {
		if err := monitoring.GenerateCloudWatchConfig(action, cfg.Metrics, cfg.NetworkInterface, cfg.DiskDevice); err != nil {
			action.Errorf("Failed to configure CloudWatch metrics: %v", err)
		}
	}

	action.Infof("Action finished.")
}

// handlePostExecution contains the logic for the post-execution phase.
func handlePostExecution(action *githubactions.Action, ctx context.Context) {
	action.Infof("Running post-execution phase...")
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Errorf("Failed to load configuration in post-execution: %v", err)
		return
	}

	// Skip all operations if not running on RunsOn runners
	if !cfg.IsUsingRunsOn() {
		action.Infof("Not running on RunsOn runner, skipping post-execution operations")
		return
	}

	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	err = costs.ComputeAndDisplayCosts(action, cfg)
	if err != nil {
		action.Warningf("Failed to compute or display costs: %v", err)
	}

	// Display metrics summary
	if cfg.HasMetrics() {
		monitoring.GenerateMetricsSummary(action, cfg.Metrics, "chart", cfg.NetworkInterface, cfg.DiskDevice)
	}

	action.Infof("Post-execution phase finished.")
}

func main() {
	ctx := context.Background()
	postFlag := flag.Bool("post", false, "Indicates the post-execution phase")
	flag.Parse()

	action := githubactions.New()

	if *postFlag {
		handlePostExecution(action, ctx)
	} else {
		handleMainExecution(action, ctx)
	}
}
