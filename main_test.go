package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/runs-on/action/internal/costs"
	"github.com/sethvargo/go-githubactions"
)

func TestClaimCostTracking(t *testing.T) {
	tests := []struct {
		name              string
		showCosts         string
		alreadyClaimed    bool
		wantTrack         bool
		wantClaimForLater bool
	}{
		{
			name:              "first enabled invocation owns costs",
			showCosts:         "inline",
			wantTrack:         true,
			wantClaimForLater: true,
		},
		{
			name:           "later enabled invocation skips costs",
			showCosts:      "summary",
			alreadyClaimed: true,
		},
		{
			name:      "disabled invocation leaves costs unclaimed",
			showCosts: "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			stateFile := filepath.Join(root, "github-state")
			envFile := filepath.Join(root, "github-env")
			t.Setenv("GITHUB_STATE", stateFile)
			t.Setenv("GITHUB_ENV", envFile)
			if tt.alreadyClaimed {
				t.Setenv(costTrackingClaimEnv, "true")
			} else {
				t.Setenv(costTrackingClaimEnv, "")
			}

			action := githubactions.New(githubactions.WithWriter(io.Discard))
			if got := claimCostTracking(action, tt.showCosts); got != tt.wantTrack {
				t.Fatalf("claimCostTracking() = %t, want %t", got, tt.wantTrack)
			}

			state, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			wantState := costTrackingStateKey + "<<"
			if !strings.Contains(string(state), wantState) ||
				!strings.Contains(string(state), strconv.FormatBool(tt.wantTrack)) {
				t.Fatalf("saved state = %q, want tracking=%t", state, tt.wantTrack)
			}

			env, err := os.ReadFile(envFile)
			if tt.wantClaimForLater {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(env), costTrackingClaimEnv+"<<") ||
					!strings.Contains(string(env), "true") {
					t.Fatalf("exported env = %q, want %s=true", env, costTrackingClaimEnv)
				}
			} else if err == nil && strings.Contains(string(env), costTrackingClaimEnv) {
				t.Fatalf("unexpected cost claim in exported env: %q", env)
			}
		})
	}
}

func TestShouldTrackCostsInPost(t *testing.T) {
	for _, tt := range []struct {
		name  string
		state string
		want  bool
	}{
		{name: "owned invocation", state: "true", want: true},
		{name: "later invocation", state: "false", want: false},
		{name: "missing state is safe", want: false},
		{name: "malformed state is safe", state: "invalid", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(costTrackingStateEnv, tt.state)
			if got := shouldTrackCostsInPost(); got != tt.want {
				t.Fatalf("shouldTrackCostsInPost() = %t, want %t", got, tt.want)
			}
		})
	}
}

// A RunsOn agent that reports costs reads the mode from a file in RUNNER_TEMP.
func TestRequestAgentCostMode(t *testing.T) {
	for _, tt := range []struct {
		name           string
		showCosts      string
		claimed        bool // this invocation owns cost reporting
		alreadyClaimed bool // an earlier invocation owns it
		previous       string
		want           string // "" means no file
	}{
		{name: "first enabled invocation sets inline", showCosts: "inline", claimed: true, want: "inline"},
		{name: "first enabled invocation sets summary", showCosts: "summary", claimed: true, want: "summary"},
		{name: "disabled invocation turns the report off", showCosts: "false", want: "off"},
		{name: "later enabled invocation overrides an earlier off", showCosts: "summary", claimed: true, previous: "off", want: "summary"},
		{name: "disabled invocation keeps an earlier enabled mode", showCosts: "false", alreadyClaimed: true, previous: "summary", want: "summary"},
		{name: "later enabled invocation keeps the first mode", showCosts: "inline", alreadyClaimed: true, previous: "summary", want: "summary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RUNNER_TEMP", t.TempDir())
			t.Setenv(costs.AgentModeFileNameEnv, "runs-on-show-costs")
			if tt.alreadyClaimed {
				t.Setenv(costTrackingClaimEnv, "true")
			} else {
				t.Setenv(costTrackingClaimEnv, "")
			}
			path := costs.AgentModeFile()
			if path == "" {
				t.Fatal("AgentModeFile() is empty although the agent advertised the file")
			}
			if tt.previous != "" {
				if err := os.WriteFile(path, []byte(tt.previous+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			requestAgentCostMode(githubactions.New(githubactions.WithWriter(io.Discard)), path, tt.showCosts, tt.claimed)

			got, err := os.ReadFile(path)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("mode file = %q, want none", got)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(got)) != tt.want {
				t.Fatalf("mode file = %q (%v), want %q", got, err, tt.want)
			}
		})
	}
}
