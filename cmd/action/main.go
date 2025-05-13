package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/runs-on/action/internal/cache"
	"github.com/runs-on/action/internal/config"
	"github.com/runs-on/action/internal/costs"
	"github.com/runs-on/action/internal/env"
	"github.com/runs-on/action/internal/kopia"
	"github.com/sethvargo/go-githubactions"
)

func getKopiaClient(ctx context.Context, logger *zerolog.Logger, version string) (*kopia.KopiaClient, error) {
	kopiaConfig := &kopia.Config{
		Region:   os.Getenv("RUNS_ON_AWS_REGION"),
		S3Bucket: os.Getenv("RUNS_ON_S3_BUCKET_CACHE"),
		Prefix:   fmt.Sprintf("cache/snapshots/%s/%s/", os.Getenv("GITHUB_REPOSITORY"), version),
		Password: "p4ssw0rd",
	}
	if kopiaConfig.Region == "" {
		return nil, fmt.Errorf("RUNS_ON_AWS_REGION is not set")
	}
	if kopiaConfig.S3Bucket == "" {
		return nil, fmt.Errorf("RUNS_ON_S3_BUCKET_CACHE is not set")
	}
	if kopiaConfig.Prefix == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is not set")
	}

	kopiaClient, err := kopia.NewKopiaClient(ctx, logger, kopiaConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kopia client: %v", err)
	}
	return kopiaClient, nil
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

	cache.UpdateZctionsConfig(action, cfg)

	if cfg.HasShowCosts() {
		action.Infof("show_costs is enabled. You will find cost details in the post-execution step of this action.")
	}

	if len(cfg.SnapshotDirs) > 0 {
		action.Infof("Restoring directories: %v", cfg.SnapshotDirs)
		kopiaClient, err := getKopiaClient(ctx, logger, cfg.SnapshotVersion)
		if err != nil {
			action.Errorf("Failed to create Kopia client: %v", err)
		} else {
			for _, dir := range cfg.SnapshotDirs {
				action.Infof("Restoring directory: %s", dir)
				err = kopiaClient.Restore(ctx, dir)
				if err != nil {
					action.Errorf("Failed to restore directory: %v", err)
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

	if len(cfg.SnapshotDirs) > 0 {
		action.Infof("Snapshotting directories: %v", cfg.SnapshotDirs)
		kopiaClient, err := getKopiaClient(ctx, logger, cfg.SnapshotVersion)
		if err != nil {
			action.Errorf("Failed to create Kopia client: %v", err)
		} else {
			for _, dir := range cfg.SnapshotDirs {
				action.Infof("Snapshotting directory: %s", dir)
				err = kopiaClient.Snapshot(ctx, dir)
				if err != nil {
					action.Errorf("Failed to snapshot directory: %v", err)
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
	logger := zerolog.New(os.Stdout)
	postFlag := flag.Bool("post", false, "Indicates the post-execution phase")
	flag.Parse()

	action := githubactions.New()

	if *postFlag {
		handlePostExecution(action, ctx, &logger)
	} else {
		handleMainExecution(action, ctx, &logger)
	}
}
