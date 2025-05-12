package costs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/runs-on/action/internal/config"
	"github.com/sethvargo/go-githubactions"
)

const costAPIURL = "https://ec2-pricing.runs-on.com/cost"
const apiTimeout = 5 * time.Second

// This file is for cost-related logic.

// CostRequestPayload matches the JSON structure expected by the cost API.
type CostRequestPayload struct {
	InstanceType      string `json:"instanceType"`
	InstanceLifecycle string `json:"instanceLifecycle"`
	Region            string `json:"region"`
	Arch              string `json:"arch"`
	StartedAt         string `json:"startedAt"`
}

// CostResponseData matches the JSON structure returned by the cost API.
type CostResponseData struct {
	InstanceType    string  `json:"instanceType"`
	Region          string  `json:"region"`
	DurationMinutes float64 `json:"durationMinutes"`
	TotalCost       float64 `json:"totalCost"`
	Github          struct {
		TotalCost float64 `json:"totalCost"`
	} `json:"github"`
	Savings struct {
		Amount     float64 `json:"amount"`
		Percentage float64 `json:"percentage"`
	} `json:"savings"`
}

// ComputeAndDisplayCosts fetches cost data and displays it based on config.
func ComputeAndDisplayCosts(action *githubactions.Action, cfg *config.Config) error {
	// Get the display costs option value (use config value)
	displayCostsOption := cfg.ShowCosts

	// Disable if not 'inline' or 'summary'
	if displayCostsOption != "inline" && displayCostsOption != "summary" {
		action.Infof("Cost calculation is disabled (show-costs=%s)", displayCostsOption)
		return nil
	}

	instanceLaunchedAt := os.Getenv("RUNS_ON_INSTANCE_LAUNCHED_AT")
	if instanceLaunchedAt == "" {
		action.Warningf("RUNS_ON_INSTANCE_LAUNCHED_AT environment variable not found. Cannot compute cost.")
		return nil // Not an error, just can't proceed
	}

	// Get runner information from environment variables
	region := os.Getenv("RUNS_ON_AWS_REGION")
	instanceType := os.Getenv("RUNS_ON_INSTANCE_TYPE")
	instanceLifecycle := os.Getenv("RUNS_ON_INSTANCE_LIFECYCLE")
	if instanceLifecycle == "" {
		instanceLifecycle = "spot" // Default to spot if not provided
	}
	instanceArchitecture := os.Getenv("RUNS_ON_AGENT_ARCH")
	if instanceArchitecture == "" {
		instanceArchitecture = "x64" // Default to x64 if not provided
	}

	// Prepare request payload
	payload := CostRequestPayload{
		InstanceType:      instanceType,
		InstanceLifecycle: instanceLifecycle,
		Region:            region,
		Arch:              instanceArchitecture,
		StartedAt:         instanceLaunchedAt,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal cost request payload: %w", err)
	}

	// Make API request with timeout
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, costAPIURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create cost API request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Consider adding a User-Agent header

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		// Check for context deadline exceeded
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("cost API request timed out after %s", apiTimeout)
		}
		return fmt.Errorf("failed to send cost API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for more details if possible
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("cost API request failed with status %s (failed to read body: %v)", resp.Status, readErr)
		}
		return fmt.Errorf("cost API request failed with status %s: %s", resp.Status, string(bodyBytes))
	}

	// Decode response
	var costData CostResponseData
	if err := json.NewDecoder(resp.Body).Decode(&costData); err != nil {
		return fmt.Errorf("failed to decode cost API response: %w", err)
	}

	// Generate formatted data strings once
	durationStr := fmt.Sprintf("%.2f minutes", costData.DurationMinutes)
	costStr := fmt.Sprintf("$%.4f", costData.TotalCost)
	githubCostStr := fmt.Sprintf("$%.4f", costData.Github.TotalCost)
	savingsStr := fmt.Sprintf("$%.4f (%.1f%%)", costData.Savings.Amount, costData.Savings.Percentage)

	// Prepare table rows
	tableRows := []table.Row{
		{"Instance Type", costData.InstanceType},
		{"Region", costData.Region},
		{"Duration", durationStr},
		{"Cost", costStr},
		{"GitHub equivalent cost", githubCostStr},
		{"Savings", savingsStr},
	}

	// Function to render markdown table to avoid repetition
	renderMarkdownTable := func(rows []table.Row) string {
		tableOutput := &strings.Builder{}
		t := table.NewWriter()
		t.SetOutputMirror(tableOutput)
		t.AppendHeader(table.Row{"Metric", "Value"})
		t.AppendRows(rows)
		t.Render()
		return tableOutput.String()
	}

	markdownTableString := renderMarkdownTable(tableRows)
	summaryBuilder := &strings.Builder{}
	summaryBuilder.WriteString("## Execution Cost Summary\n\n")
	summaryBuilder.WriteString(markdownTableString)
	summaryBuilder.WriteString("\n") // Add a newline for spacing

	fmt.Print(summaryBuilder.String())

	if displayCostsOption == "summary" {
		action.AddStepSummary(summaryBuilder.String())
		action.Infof("Cost summary added to job summary.")
	}

	return nil
}
