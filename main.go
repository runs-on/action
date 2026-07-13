package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/runs-on/action/internal/cache"
	"github.com/runs-on/action/internal/config"
	"github.com/runs-on/action/internal/costs"
	"github.com/runs-on/action/internal/env"
	"github.com/runs-on/action/internal/gitproxy"
	"github.com/runs-on/action/internal/monitoring"
	"github.com/runs-on/action/internal/sccache"
	"github.com/runs-on/action/internal/stickydisk"
	"github.com/sethvargo/go-githubactions"
)

// handleMainExecution contains the original main logic.
func handleMainExecution(action *githubactions.Action, ctx context.Context) {
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Fatalf("Failed to load configuration: %v", err)
	}
	stickydisk.SetBuildkitOutputs(action)

	// Execute logic based on configuration
	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	cache.UpdateZctionsConfig(action, cfg.ActionsResultsURL, cfg.ZctionsResultsURL, cfg.ZctionsCacheURL, cfg.ActionsRuntimeToken)

	if cfg.HasShowCosts() {
		action.Infof("show_costs is enabled. You will find cost details in the post-execution step of this action.")
	}

	// Configure sccache if requested
	if cfg.HasSccache() {
		if err := sccache.ConfigureSccache(action, cfg.Sccache); err != nil {
			action.Errorf("Failed to configure sccache: %v", err)
		}
	}

	// Configure sticky disk cache mounts if requested
	if cfg.HasStickyDiskCache() {
		if err := stickydisk.Configure(action, stickydisk.Options{
			StickyCache:       cfg.StickyCache,
			StickyWaitTimeout: cfg.StickyWaitTimeout,
		}); err != nil {
			action.Fatalf("Failed to configure sticky disk cache: %v", err)
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

	// Run cache mode post-job hooks (e.g. stop the Buildx builder) before the sticky
	// disk is unmounted and snapshotted, then display usage
	if cfg.HasStickyDiskCache() {
		postErr := stickydisk.PostJob(action, cfg.StickyCache)
		stickydisk.DisplayUsage(action)
		if postErr != nil {
			action.Fatalf("Sticky disk post-job cleanup failed: %v", postErr)
		}
	}

	action.Infof("Post-execution phase finished.")
}

func main() {
	ctx := context.Background()
	postFlag := flag.Bool("post", false, "Indicates the post-execution phase")
	gitProxyFlag := flag.Bool("git-proxy-serve", false, "Run the local git mirror proxy (internal, spawned by the git cache mode)")
	flag.Parse()

	if *gitProxyFlag {
		if err := gitproxy.RunFromEnv(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "git proxy failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	action := githubactions.New()

	if *postFlag {
		handlePostExecution(action, ctx)
	} else {
		handleMainExecution(action, ctx)
	}
}
