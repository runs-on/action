package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/guptarohit/asciigraph"
	"github.com/sethvargo/go-githubactions"
)

// const NAMESPACE = "RunsOn/Runners"

const NAMESPACE = "CWAgent"

// https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1#InstanceDetails:instanceId=i-03ac2c780bf1d5a42
// https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#metricsV2?graph=~()&namespace=~'CWAgent&query=~'InstanceId*3d*22i-03ac2c780bf1d5a42*22

type CloudWatchConfig struct {
	Metrics MetricsConfig `json:"metrics"`
}

type MetricsConfig struct {
	Namespace        string                 `json:"namespace"`
	MetricsCollected map[string]interface{} `json:"metrics_collected"`
	AppendDimensions map[string]string      `json:"append_dimensions"`
}

type MetricDataPoint struct {
	Timestamp time.Time
	Value     float64
}

type MetricSummary struct {
	Name   string
	Data   []float64
	Min    float64
	Max    float64
	Avg    float64
	Unit   string
	Source string // "AWS" or "Custom"
}

func GenerateCloudWatchConfig(action *githubactions.Action, metrics []string) error {
	if len(metrics) == 0 {
		return nil
	}

	// Enable detailed monitoring for the instance
	if err := enableDetailedMonitoring(action); err != nil {
		action.Warningf("Failed to enable detailed monitoring: %v", err)
	}

	config := CloudWatchConfig{
		Metrics: MetricsConfig{
			Namespace:        NAMESPACE,
			MetricsCollected: make(map[string]interface{}),
			AppendDimensions: map[string]string{
				"InstanceId": "${aws:InstanceId}",
			},
		},
	}

	// Configure metrics based on input with more frequent collection for detailed monitoring
	for _, metric := range metrics {
		switch strings.ToLower(metric) {
		case "memory":
			config.Metrics.MetricsCollected["mem"] = map[string]interface{}{
				"drop_original_metrics": true,
				"measurement": []string{
					"used_percent",
					"available_percent",
					"total",
					"used",
				},
				"metrics_collection_interval": 10, // Keep aggressive collection
			}
		case "disk":
			config.Metrics.MetricsCollected["disk"] = map[string]interface{}{
				"drop_original_metrics": true,
				"drop_device":           true,
				"measurement": []string{
					"used_percent",
				},
				"resources": []string{"/", "/tmp", "/var/lib/docker", "/home/runner"},
				"ignore_file_system_types": []string{
					"sysfs", "devtmpfs",
				},
				"metrics_collection_interval": 30, // More frequent for detailed monitoring
			}
		case "io":
			config.Metrics.MetricsCollected["diskio"] = map[string]interface{}{
				"drop_original_metrics": true,
				"measurement": []string{
					"reads",
					"writes",
					"read_bytes",
					"write_bytes",
					"read_time",
					"write_time",
					"io_time",
				},
				"resources":                   []string{"nvme0n1p1"},
				"metrics_collection_interval": 10, // Keep high frequency
			}
		}
	}

	// Write config file
	configFile, err := os.CreateTemp("", "runs-on-metrics-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	configPath := configFile.Name()
	defer configFile.Close()

	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	action.Infof("Generated CloudWatch config with metrics: %v", metrics)
	action.Infof("Config file: %s", configPath)
	action.Infof("🔗 CloudWatch link: %s", GetCloudWatchDashboardURL(action))

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
	action.Infof("CloudWatch agent output: %s", string(output))

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

	// https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1#InstanceDetails:instanceId=i-03ac2c780bf1d5a42
	return fmt.Sprintf("https://%s.console.aws.amazon.com/ec2/home?region=%s#InstanceDetails:instanceId=%s",
		region, region, instanceID)
}

func GenerateMetricsSummary(action *githubactions.Action, metrics []string, formatter string) {
	if len(metrics) == 0 {
		return
	}

	// Default formatter to "chart" if empty
	if formatter == "" {
		formatter = "chart"
	}

	// parsing: 2025-06-05T12:05:32+02:00
	launchTimeRaw, ok := os.LookupEnv("RUNS_ON_INSTANCE_LAUNCHED_AT")
	if !ok {
		action.Warningf("RUNS_ON_INSTANCE_LAUNCHED_AT is not set, cannot fetch metrics")
		return
	}

	launchTime, err := time.Parse(time.RFC3339, launchTimeRaw)
	if err != nil {
		action.Warningf("Failed to parse RUNS_ON_INSTANCE_LAUNCHED_AT: %v", err)
		return
	}

	action.Infof("## CloudWatch Metrics Summary (format: %s)", formatter)
	action.Infof("Enabled metrics: cpu, network, %s\n", strings.Join(metrics, ", "))
	action.Infof("Namespace: %s\n", NAMESPACE)
	action.Infof("🔗 CloudWatch link: %s\n", GetCloudWatchDashboardURL(action))

	// Fetch and display metrics with sparklines
	collector := NewMetricsCollector(action)
	if collector == nil {
		action.Warningf("Could not initialize metrics collector")
		return
	}

	action.Infof("📈 Metrics (since %s):", launchTime.Format(time.RFC3339))

	// AWS default metrics (always available)
	awsMetrics := []struct {
		name      string
		awsName   string
		unit      string
		namespace string
	}{
		{"CPU", "CPUUtilization", "%", "AWS/EC2"},
		{"NetworkIn", "NetworkIn", "bytes", "AWS/EC2"},
		{"NetworkOut", "NetworkOut", "bytes", "AWS/EC2"},
	}

	// Display AWS metrics
	for _, metric := range awsMetrics {
		summary := collector.GetMetricSummary(metric.awsName, metric.namespace, []types.Dimension{}, launchTime)
		displayMetric(action, metric.name, summary, metric.unit, formatter)
	}

	// Display custom metrics if enabled
	for _, metricType := range metrics {
		switch strings.ToLower(metricType) {
		case "memory":
			summary := collector.GetMetricSummary("mem_used_percent", NAMESPACE, []types.Dimension{}, launchTime)
			displayMetric(action, "Memory", summary, "%", formatter)
		case "disk":
			for _, path := range []string{"/", "/tmp", "/var/lib/docker", "/home/runner"} {
				summary := collector.GetMetricSummary("disk_used_percent", NAMESPACE, []types.Dimension{
					{
						Name:  aws.String("path"),
						Value: aws.String(path),
					},
					{
						Name:  aws.String("fstype"),
						Value: aws.String("ext4"),
					},
				}, launchTime)
				// some paths might not have mount points
				if path != "/" && summary == nil {
					continue
				}
				displayMetric(action, fmt.Sprintf("Disk used %% (%s)", path), summary, "%", formatter)
			}
		case "io":
			summaryReads := collector.GetMetricSummary("diskio_reads", NAMESPACE, []types.Dimension{
				{
					Name:  aws.String("name"),
					Value: aws.String("nvme0n1p1"),
				},
			}, launchTime)
			summaryWrites := collector.GetMetricSummary("diskio_writes", NAMESPACE, []types.Dimension{
				{
					Name:  aws.String("name"),
					Value: aws.String("nvme0n1p1"),
				},
			}, launchTime)
			displayMetric(action, fmt.Sprintf("Disk Reads (%s)", "nvme0n1p1"), summaryReads, "ops/s", formatter)
			displayMetric(action, fmt.Sprintf("Disk Writes (%s)", "nvme0n1p1"), summaryWrites, "ops/s", formatter)
		}
	}
}

// displayMetric shows a metric in the specified format (sparkline or chart)
func displayMetric(action *githubactions.Action, name string, summary *MetricSummary, unit string, formatter string) {
	if summary == nil {
		action.Infof("  %-12s ─────────────── (no data)", name)
		return
	}

	if formatter == "chart" {
		action.Infof("\n📊 %s Chart:", name)
		caption := fmt.Sprintf("%s (%s)", name, unit)
		graph := asciigraph.Plot(summary.Data,
			asciigraph.Height(8),
			asciigraph.Width(60),
			asciigraph.Caption(caption),
			asciigraph.Precision(1),
		)
		// Print each line of the graph with proper indentation
		for _, line := range strings.Split(graph, "\n") {
			action.Infof("  %s", line)
		}
		action.Infof("  Stats: min:%.1f avg:%.1f max:%.1f %s", summary.Min, summary.Avg, summary.Max, unit)
	} else {
		// Use sparkline format
		sparkline := createSparkline(summary.Data)
		if unit == "ops/s" {
			action.Infof("  %-12s %s avg:%.0f %s",
				name, sparkline, summary.Avg, unit)
		} else {
			action.Infof("  %-12s %s min:%.1f avg:%.1f max:%.1f %s",
				name, sparkline, summary.Min, summary.Avg, summary.Max, unit)
		}
	}
	action.Infof("\n")
}

// calculateMin returns the minimum value in a slice
func calculateMin(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	min := data[0]
	for _, v := range data {
		if v < min {
			min = v
		}
	}
	return min
}

// calculateMax returns the maximum value in a slice
func calculateMax(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	max := data[0]
	for _, v := range data {
		if v > max {
			max = v
		}
	}
	return max
}

type MetricsCollector struct {
	cwClient   *cloudwatch.Client
	instanceID string
	action     *githubactions.Action
}

func NewMetricsCollector(action *githubactions.Action) *MetricsCollector {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		action.Warningf("Failed to load AWS config: %v", err)
		return nil
	}

	instanceID := os.Getenv("RUNS_ON_INSTANCE_ID")
	if instanceID == "" {
		action.Warningf("RUNS_ON_INSTANCE_ID not set, cannot fetch metrics")
		return nil
	}

	return &MetricsCollector{
		cwClient:   cloudwatch.NewFromConfig(cfg),
		instanceID: instanceID,
		action:     action,
	}
}

func (mc *MetricsCollector) GetMetricSummary(metricName, namespace string, dimensions []types.Dimension, startTime time.Time) *MetricSummary {
	data, err := mc.getMetricData(metricName, namespace, dimensions, startTime)
	if err != nil {
		mc.action.Infof("Failed to get metric %s: %v", metricName, err)
		return nil
	}

	if len(data) == 0 {
		return nil
	}

	// Extract values and calculate stats
	values := make([]float64, len(data))
	for i, dp := range data {
		values[i] = dp.Value
	}

	min, max, avg := calculateStats(values)

	return &MetricSummary{
		Name: metricName,
		Data: values,
		Min:  min,
		Max:  max,
		Avg:  avg,
	}
}

func (mc *MetricsCollector) getMetricData(metricName, namespace string, dimensions []types.Dimension, startTime time.Time) ([]MetricDataPoint, error) {
	endTime := time.Now()

	input := &cloudwatch.GetMetricDataInput{
		MetricDataQueries: []types.MetricDataQuery{
			{
				Id: aws.String("m1"),
				MetricStat: &types.MetricStat{
					Metric: &types.Metric{
						Namespace:  aws.String(namespace),
						MetricName: aws.String(metricName),
						Dimensions: append(dimensions, []types.Dimension{
							{
								Name:  aws.String("InstanceId"),
								Value: aws.String(mc.instanceID),
							},
						}...),
					},
					Period: aws.Int32(10), // 10 seconds granularity for raw data
					Stat:   aws.String("Average"),
				},
				ReturnData: aws.Bool(true),
			},
		},
		StartTime: aws.Time(startTime),
		EndTime:   aws.Time(endTime),
	}

	result, err := mc.cwClient.GetMetricData(context.Background(), input)
	if err != nil {
		return nil, err
	}

	if len(result.MetricDataResults) == 0 || len(result.MetricDataResults[0].Values) == 0 {
		return nil, nil
	}

	metricResult := result.MetricDataResults[0]
	var points []MetricDataPoint

	// Combine timestamps and values
	for i, value := range metricResult.Values {
		if i < len(metricResult.Timestamps) {
			points = append(points, MetricDataPoint{
				Timestamp: metricResult.Timestamps[i],
				Value:     value,
			})
		}
	}

	// Sort by timestamp
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.Before(points[j].Timestamp)
	})

	return points, nil
}

// createSparkline generates a Unicode sparkline from data
func createSparkline(values []float64) string {
	if len(values) == 0 {
		return "─"
	}

	// Limit to reasonable sparkline length
	maxLength := 15
	if len(values) > maxLength {
		// Sample evenly across the data
		step := len(values) / maxLength
		sampled := make([]float64, 0, maxLength)
		for i := 0; i < len(values); i += step {
			sampled = append(sampled, values[i])
		}
		values = sampled
	}

	sparkChars := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	min, max := values[0], values[0]
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}

	var result strings.Builder
	for _, v := range values {
		if max == min {
			result.WriteRune('▄') // Middle character for flat data
		} else {
			index := int((v - min) / (max - min) * float64(len(sparkChars)-1))
			if index >= len(sparkChars) {
				index = len(sparkChars) - 1
			}
			if index < 0 {
				index = 0
			}
			result.WriteRune(sparkChars[index])
		}
	}

	return result.String()
}

// calculateStats computes min, max, and average of a slice of floats
func calculateStats(data []float64) (min, max, avg float64) {
	if len(data) == 0 {
		return 0, 0, 0
	}

	min, max = data[0], data[0]
	sum := 0.0

	for _, v := range data {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}

	avg = sum / float64(len(data))
	return
}

func enableDetailedMonitoring(action *githubactions.Action) error {
	instanceID := os.Getenv("RUNS_ON_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("RUNS_ON_INSTANCE_ID not set")
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := ec2.NewFromConfig(cfg)

	// Enable detailed monitoring
	input := &ec2.MonitorInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := ec2Client.MonitorInstances(context.Background(), input)
	if err != nil {
		return fmt.Errorf("failed to enable detailed monitoring: %w", err)
	}

	if len(result.InstanceMonitorings) > 0 {
		monitoring := result.InstanceMonitorings[0]
		action.Infof("✅ Detailed monitoring enabled for instance %s (state: %s)",
			*monitoring.InstanceId, monitoring.Monitoring.State)
	}

	return nil
}
