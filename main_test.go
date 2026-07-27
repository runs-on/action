package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
		{name: "missing legacy state", want: true},
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
