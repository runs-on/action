package main

import (
	"context"
	"flag"
	"os"

	"github.com/rs/zerolog"
	"github.com/runs-on/action/internal/cache"
	"github.com/runs-on/action/internal/config"
	"github.com/runs-on/action/internal/costs"
	"github.com/runs-on/action/internal/env"
	"github.com/runs-on/action/internal/snapshot"
	"github.com/sethvargo/go-githubactions"
)

var snapshotterConfig = snapshot.SnapshotterConfig{
	GithubRef:                 os.Getenv("GITHUB_REF"),
	InstanceID:                os.Getenv("RUNS_ON_INSTANCE_ID"),
	Az:                        os.Getenv("RUNS_ON_AWS_AZ"),
	WaitForSnapshotCompletion: false,
}

// handleMainExecution contains the original main logic.
func handleMainExecution(action *githubactions.Action, ctx context.Context, logger *zerolog.Logger) {
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Fatalf("Failed to load configuration: %v", err)
	}

	// Execute logic based on configuration
	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	cache.UpdateZctionsConfig(action, cfg.ActionsResultsURL, cfg.ZctionsResultsURL)

	if cfg.HasShowCosts() {
		action.Infof("show_costs is enabled. You will find cost details in the post-execution step of this action.")
	}

	if len(cfg.SnapshotDirs) > 0 {
		if cfg.RunnerConfig == nil {
			action.Warningf("No runner config provided (you need to upgrade your RunsOn installation). Snapshotting will not be performed.")
		} else {
			snapshotterConfig.DefaultBranch = cfg.RunnerConfig.DefaultBranch
			snapshotterConfig.CustomTags = cfg.RunnerConfig.CustomTags
			snapshotter, err := snapshot.NewAWSSnapshotter(ctx, logger, snapshotterConfig)
			if err != nil {
				action.Errorf("Failed to create snapshotter: %v", err)
			} else {
				for _, dir := range cfg.SnapshotDirs {
					action.Infof("Creating snapshot for %s", dir)
					_, err := snapshotter.RestoreSnapshot(ctx, dir)
					if err != nil {
						action.Errorf("Failed to restore snapshot for %s: %v", dir, err)
					}
				}
			}
		}
	}

	action.Infof("Action finished.")
}

// handlePostExecution contains the logic for the post-execution phase.
func handlePostExecution(action *githubactions.Action, ctx context.Context, logger *zerolog.Logger) {
	action.Infof("Running post-execution phase...")
	cfg, err := config.NewConfigFromInputs(action)
	if err != nil {
		action.Errorf("Failed to load configuration in post-execution: %v", err)
		return
	}

	if cfg.HasShowEnv() {
		env.DisplayEnvVars()
	}

	if len(cfg.SnapshotDirs) > 0 {
		if cfg.RunnerConfig == nil {
			action.Warningf("No runner config provided (you need to upgrade your RunsOn installation). Snapshotting will not be performed.")
		} else {
			action.Infof("Snapshotting volumes...")
			snapshotter, err := snapshot.NewAWSSnapshotter(ctx, logger, snapshotterConfig)
			if err != nil {
				action.Errorf("Failed to create snapshotter: %v", err)
			} else {
				for _, dir := range cfg.SnapshotDirs {
					snapshot, err := snapshotter.CreateSnapshot(ctx, dir)
					if err != nil {
						action.Errorf("Failed to snapshot volumes: %v", err)
						continue
					}
					action.Infof("Snapshot created: %s. Note that it might take a few minutes to be available for use.", snapshot.SnapshotID)
				}
			}
		}
	}
	err = costs.ComputeAndDisplayCosts(action, cfg)
	if err != nil {
		action.Errorf("Failed to compute or display costs: %v", err)
	}
	action.Infof("Post-execution phase finished.")
}

func main() {
	ctx := context.Background()
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	postFlag := flag.Bool("post", false, "Indicates the post-execution phase")
	flag.Parse()

	action := githubactions.New()

	if *postFlag {
		handlePostExecution(action, ctx, &logger)
	} else {
		handleMainExecution(action, ctx, &logger)
	}
}
