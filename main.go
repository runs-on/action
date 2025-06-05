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
	"github.com/sethvargo/go-githubactions"
)

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
