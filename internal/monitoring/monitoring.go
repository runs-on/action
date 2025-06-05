package monitoring

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

type CloudWatchConfig struct {
	Metrics MetricsConfig `json:"metrics"`
}

type MetricsConfig struct {
	Namespace        string                 `json:"namespace"`
	MetricsCollected map[string]interface{} `json:"metrics_collected"`
	AppendDimensions map[string]string      `json:"append_dimensions"`
}

func GenerateCloudWatchConfig(action *githubactions.Action, metrics []string) error {
	if len(metrics) == 0 {
		return nil
	}

	config := CloudWatchConfig{
		Metrics: MetricsConfig{
			Namespace:        "RunsOn/Action",
			MetricsCollected: make(map[string]interface{}),
			AppendDimensions: map[string]string{
				"InstanceId":   "${aws:InstanceId}",
				"InstanceType": "${aws:InstanceType}",
			},
		},
	}

	// Configure metrics based on input
	for _, metric := range metrics {
		switch strings.ToLower(metric) {
		case "memory":
			config.Metrics.MetricsCollected["mem"] = map[string]interface{}{
				"measurement": []string{
					"mem_used_percent",
					"mem_available_percent",
					"mem_total",
					"mem_used",
				},
				"metrics_collection_interval": 60,
			}
		case "disk":
			config.Metrics.MetricsCollected["disk"] = map[string]interface{}{
				"measurement": []string{
					"used_percent",
					"free",
					"total",
					"used",
				},
				"resources": []string{"*"},
				"ignore_file_system_types": []string{
					"sysfs", "devtmpfs", "tmpfs",
				},
				"metrics_collection_interval": 60,
			}
		case "io":
			config.Metrics.MetricsCollected["diskio"] = map[string]interface{}{
				"measurement": []string{
					"reads",
					"writes",
					"read_bytes",
					"write_bytes",
					"read_time",
					"write_time",
					"io_time",
				},
				"resources":                   []string{"*"},
				"metrics_collection_interval": 60,
			}
		}
	}

	// Write config file
	configPath := "/tmp/runs-on-metrics.json"
	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	action.Infof("Generated CloudWatch config with metrics: %v", metrics)

	// Append the config to the running CloudWatch agent
	return appendCloudWatchConfig(action, configPath)
}

func appendCloudWatchConfig(action *githubactions.Action, configPath string) error {
	// Check if CloudWatch agent is available
	agentCtl := "/opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl"
	if _, err := os.Stat(agentCtl); os.IsNotExist(err) {
		action.Warningf("CloudWatch agent not found at %s, skipping metrics configuration", agentCtl)
		return nil
	}

	// Append the configuration to the running agent
	cmd := exec.Command("sudo", agentCtl,
		"-a", "append-config",
		"-m", "ec2",
		"-s",
		"-c", fmt.Sprintf("file:%s", configPath))

	output, err := cmd.CombinedOutput()
	if err != nil {
		action.Warningf("Failed to append CloudWatch config: %v\nOutput: %s", err, string(output))
		return err
	}

	action.Infof("Successfully appended CloudWatch metrics configuration")
	action.Debugf("CloudWatch agent output: %s", string(output))

	return nil
}

func GetCloudWatchDashboardURL(action *githubactions.Action) string {
	region := os.Getenv("RUNS_ON_AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	instanceID := os.Getenv("RUNS_ON_INSTANCE_ID")
	if instanceID == "" {
		action.Fatalf("RUNS_ON_INSTANCE_ID is not set")
	}

	return fmt.Sprintf("https://%s.console.aws.amazon.com/cloudwatch/home?region=%s#metricsV2:graph=~();search=RunsOn~2FAction;namespace=RunsOn~2FAction;dimensions=InstanceId:%s",
		region, region, instanceID)
}

func GenerateMetricsSummary(action *githubactions.Action, metrics []string) {
	if len(metrics) == 0 {
		return
	}

	action.Infof("📊 CloudWatch Metrics Summary")
	action.Infof("Enabled metrics: %s", strings.Join(metrics, ", "))
	action.Infof("Namespace: RunsOn/Action")
	action.Infof("🔗 CloudWatch Dashboard: %s", GetCloudWatchDashboardURL(action))

	// Show what metrics are being collected
	for _, metric := range metrics {
		switch strings.ToLower(metric) {
		case "memory":
			action.Infof("  Memory: mem_used_percent, mem_available_percent, mem_total, mem_used")
		case "disk":
			action.Infof("  Disk: used_percent, free, total, used (excluding system filesystems)")
		case "io":
			action.Infof("  I/O: reads, writes, read_bytes, write_bytes, read_time, write_time, io_time")
		}
	}
}
