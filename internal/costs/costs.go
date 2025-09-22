package costs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/runs-on/action/internal/config"
	"github.com/runs-on/action/internal/utils"
	"github.com/sethvargo/go-githubactions"
)

const costAPIURL = "https://go.runs-on.com/api/costs"
const apiTimeout = 5 * time.Second

// This file is for cost-related logic.

// CostRequestPayload matches the JSON structure expected by the cost API.
type CostRequestPayload struct {
	InstanceType      string `json:"instanceType"`
	InstanceLifecycle string `json:"instanceLifecycle"`
	Region            string `json:"region"`
	Az                string `json:"az"`
	ZoneId            string `json:"zoneId,omitempty"`
	Arch              string `json:"arch"`
	StartedAt         string `json:"startedAt"`
	Platform          string `json:"platform"`
}

// CostResponseData matches the JSON structure returned by the cost API.
type CostResponseData struct {
	InstanceType      string  `json:"instanceType"`
	Region            string  `json:"region"`
	Platform          string  `json:"platform"`
	Arch              string  `json:"arch"`
	Az                string  `json:"az"`
	ZoneId            string  `json:"zoneId"`
	InstanceLifecycle string  `json:"instanceLifecycle"`
	DurationMinutes   float64 `json:"durationMinutes"`
	TotalCost         float64 `json:"totalCost"`
	Github            struct {
		TotalCost float64 `json:"totalCost"`
	} `json:"github"`
	Savings struct {
		Amount     float64 `json:"amount"`
		Percentage float64 `json:"percentage"`
	} `json:"savings"`
}

// getZoneIdFromZoneName maps an availability zone name to its zone ID using AWS API
func getZoneIdFromZoneName(zoneName, region string) (string, error) {
	if zoneName == "" || region == "" {
		return "", fmt.Errorf("zone name and region are required")
	}

	cfg, err := utils.GetAWSClientFromEC2IMDS(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(*cfg)

	input := &ec2.DescribeAvailabilityZonesInput{
		ZoneNames: []string{zoneName},
	}

	result, err := ec2Client.DescribeAvailabilityZones(context.Background(), input)
	if err != nil {
		return "", fmt.Errorf("failed to describe availability zones: %w", err)
	}

	if len(result.AvailabilityZones) == 0 {
		return "", fmt.Errorf("no availability zone found for zone name: %s", zoneName)
	}

	zoneId := aws.ToString(result.AvailabilityZones[0].ZoneId)
	if zoneId == "" {
		return "", fmt.Errorf("zone ID not found for zone name: %s", zoneName)
	}

	return zoneId, nil
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
	az := os.Getenv("RUNS_ON_AWS_AZ")
	instanceType := os.Getenv("RUNS_ON_INSTANCE_TYPE")
	instanceLifecycle := os.Getenv("RUNS_ON_INSTANCE_LIFECYCLE")
	if instanceLifecycle == "" {
		instanceLifecycle = "spot" // Default to spot if not provided
	}
	instanceArchitecture := os.Getenv("RUNS_ON_AGENT_ARCH")
	if instanceArchitecture == "" {
		instanceArchitecture = "x64" // Default to x64 if not provided
	}

	platform := runtime.GOOS

	// Attempt to find the zone ID mapping for the zone name
	zoneId := "" // Default to empty string if mapping fails
	if az != "" && region != "" {
		if mappedZoneId, err := getZoneIdFromZoneName(az, region); err != nil {
			// FIXME: change to warning 2 months after v2.8.4 release of RunsOn
			action.Infof("Failed to get zone ID for zone %s: %v. Using zone name instead, report might not be completely accurate.", az, err)
		} else {
			zoneId = mappedZoneId
			action.Infof("Mapped zone name %s to zone ID %s", az, zoneId)
		}
	}

	// Prepare request payload
	payload := CostRequestPayload{
		InstanceType:      instanceType,
		InstanceLifecycle: instanceLifecycle,
		Region:            region,
		Az:                az,
		ZoneId:            zoneId, // Use zone ID instead of zone name
		Arch:              instanceArchitecture,
		StartedAt:         instanceLaunchedAt,
		Platform:          platform,
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

	headers := []string{"metric", "value"}
	rows := [][]string{
		{"Instance Type", costData.InstanceType},
		{"Instance Lifecycle", costData.InstanceLifecycle},
		{"Region", costData.Region},
		{"Platform", costData.Platform},
		{"Arch", costData.Arch},
		{"Az", costData.Az},
		{"Zone ID", costData.ZoneId},
		{"Duration", durationStr},
		{"Cost", costStr},
		{"GitHub equivalent cost", githubCostStr},
		{"Savings", savingsStr},
	}
	markdownTableString := renderMarkdownTable(headers, rows)

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

// not using a proper markdown library (yet)
func renderMarkdownTable(headers []string, rows [][]string) string {
	// Find max width for each column
	colWidths := make([]int, len(headers))
	for i, h := range headers {
		colWidths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > colWidths[i] {
				colWidths[i] = len(cell)
			}
		}
	}

	// Helper to pad a row
	padRow := func(row []string) string {
		out := "|"
		for i, cell := range row {
			out += " " + cell + strings.Repeat(" ", colWidths[i]-len(cell)) + " |"
		}
		return out
	}

	// Build separator
	sep := "|"
	for _, w := range colWidths {
		sep += " " + strings.Repeat("-", w) + " |"
	}

	var b strings.Builder
	b.WriteString(padRow(headers) + "\n")
	b.WriteString(sep + "\n")
	for _, row := range rows {
		b.WriteString(padRow(row) + "\n")
	}
	return b.String()
}
