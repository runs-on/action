package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

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

const (
	costTrackingClaimEnv = "RUNS_ON_ACTION_COST_TRACKING_CLAIMED"
	costTrackingStateKey = "runs_on_action_track_costs"
	costTrackingStateEnv = "STATE_" + costTrackingStateKey
)

func costTrackingRequested(showCosts string) bool {
	return showCosts == "inline" || showCosts == "summary"
}

// claimCostTracking assigns cost reporting to the first enabled invocation in
// a job. GITHUB_ENV tells later action steps that the job already has an owner,
// while GITHUB_STATE carries the decision to this invocation's own post step.
func claimCostTracking(action *githubactions.Action, showCosts string) bool {
	track := costTrackingRequested(showCosts) &&
		!strings.EqualFold(strings.TrimSpace(os.Getenv(costTrackingClaimEnv)), "true")
	action.SaveState(costTrackingStateKey, strconv.FormatBool(track))
	if track {
		action.SetEnv(costTrackingClaimEnv, "true")
	}
	return track
}

func shouldTrackCostsInPost() bool {
	state := strings.TrimSpace(os.Getenv(costTrackingStateEnv))
	track, err := strconv.ParseBool(state)
	return err == nil && track
}

// handleMainExecution contains the original main logic.
func handleMainExecution(action *githubactions.Action, ctx context.Context) {
	if healthToken := os.Getenv(gitproxy.EnvHealthToken); healthToken != "" {
		action.AddMask(healthToken)
	}
	stickydisk.SetDefaultOutputs(action)
	if err := stickydisk.RestoreStaleGitProxyRewrites(); err != nil {
		action.Fatalf("Failed to restore stale Git proxy configuration: %v", err)
	}
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Fatalf("Failed to load configuration: %v", err)
	}

	// Execute logic based on configuration
	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	cache.UpdateZctionsConfig(action, cfg.ActionsResultsURL, cfg.ZctionsResultsURL, cfg.ZctionsCacheURL, cfg.ActionsRuntimeToken)

	if claimCostTracking(action, cfg.ShowCosts) {
		action.Infof("show_costs is enabled. You will find cost details in the post-execution step of this action.")
	} else if costTrackingRequested(cfg.ShowCosts) {
		action.Infof("Cost tracking is already registered by an earlier runs-on/action invocation; this invocation will skip duplicate cost reporting.")
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

	if shouldTrackCostsInPost() {
		err = costs.ComputeAndDisplayCosts(action, cfg)
		if err != nil {
			action.Warningf("Failed to compute or display costs: %v", err)
		}
	} else if costTrackingRequested(cfg.ShowCosts) {
		action.Infof("Skipping duplicate cost reporting from this runs-on/action invocation.")
	} else {
		action.Infof("Cost reporting is disabled for this runs-on/action invocation.")
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
