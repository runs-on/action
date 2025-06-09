package monitoring

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/guptarohit/asciigraph"
	"github.com/sethvargo/go-githubactions"
)

const NAMESPACE = "CWAgent"

type CloudWatchConfig struct {
	Metrics MetricsConfig `json:"metrics"`
	Agent   AgentConfig   `json:"agent"`
}

type MetricsConfig struct {
	Namespace          string                 `json:"namespace"`
	MetricsCollected   map[string]interface{} `json:"metrics_collected"`
	AppendDimensions   map[string]string      `json:"append_dimensions"`
	ForceFlushInterval int                    `json:"force_flush_interval"`
}

type AgentConfig struct {
	MetricsCollectionInterval int `json:"metrics_collection_interval"`
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

type Measurement struct {
	Name        string
	RealName    string
	Rename      string
	Unit        string
	Aggregation string
}

// GetMetricNames returns a list of metric names for a given resource type
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/metrics-collected-by-CloudWatch-agent.html
func GetMeasurements(metric string) []Measurement {
	switch metric {
	case "cpu":
		return []Measurement{
			{
				Name:        "usage_user",
				RealName:    "cpu_usage_user",
				Rename:      "CPU User",
				Unit:        "Percent",
				Aggregation: "Average",
			},
			{
				Name:        "usage_system",
				RealName:    "cpu_usage_system",
				Rename:      "CPU System",
				Unit:        "Percent",
				Aggregation: "Average",
			},
		}
	case "network":
		return []Measurement{
			{
				Name:        "bytes_recv",
				RealName:    "net_bytes_recv",
				Rename:      "Network Received",
				Unit:        "Bytes",
				Aggregation: "Sum",
			},
			{
				Name:        "bytes_sent",
				RealName:    "net_bytes_sent",
				Rename:      "Network Sent",
				Unit:        "Bytes",
				Aggregation: "Sum",
			},
		}
	case "memory":
		return []Measurement{
			{
				Name:        "used_percent",
				RealName:    "mem_used_percent",
				Rename:      "Memory Used",
				Unit:        "Percent",
				Aggregation: "Average",
			},
		}
	case "disk":
		return []Measurement{
			{
				Name:        "used_percent",
				RealName:    "disk_used_percent",
				Rename:      "Disk Used",
				Unit:        "Percent",
				Aggregation: "Average",
			},
			{
				Name:        "inodes_used",
				RealName:    "disk_inodes_used",
				Rename:      "Disk Inodes Used",
				Unit:        "Inodes",
				Aggregation: "Sum",
			},
		}
	case "io":
		return []Measurement{
			{
				Name:        "io_time",
				RealName:    "diskio_io_time",
				Rename:      "Disk IO Time",
				Unit:        "ms",
				Aggregation: "Sum",
			},
			{
				Name:        "reads",
				RealName:    "diskio_reads",
				Rename:      "Disk Reads",
				Unit:        "Ops/s",
				Aggregation: "Sum",
			},
			{
				Name:        "writes",
				RealName:    "diskio_writes",
				Rename:      "Disk Writes",
				Unit:        "Ops/s",
				Aggregation: "Sum",
			},
		}
	default:
		return nil
	}
}

func GenerateMetricsSummary(action *githubactions.Action, metrics []string, formatter, networkInterface, diskDevice string) {
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

	// Get network interface and disk device based on config
	networkInterface = getNetworkInterface(networkInterface)
	diskDevice = getDiskDevice(diskDevice)

	action.Infof("## CloudWatch Metrics Summary\n")
	action.Infof("Enabled metrics: %s", strings.Join(metrics, ", "))
	action.Infof("Namespace: %s", NAMESPACE)
	action.Infof("Network interface: %s", networkInterface)
	action.Infof("Disk device: %s", diskDevice)
	action.Infof("")
	showLinks(action, metrics)

	// Fetch and display metrics with sparklines
	collector := NewMetricsCollector(action)
	if collector == nil {
		action.Warningf("Could not initialize metrics collector")
		return
	}

	action.Infof("📈 Metrics (since %s):", launchTime.Format(time.RFC3339))

	for _, formatter := range []string{"chart"} {
		action.Infof("")
		// Display custom metrics if enabled
		for _, metricType := range metrics {
			measurements := GetMeasurements(metricType)
			for _, measurement := range measurements {
				dimensions := []types.Dimension{}
				variants := []string{"default"}
				if metricType == "cpu" {
					dimensions = append(dimensions, types.Dimension{
						Name:  aws.String("cpu"),
						Value: aws.String("cpu-total"),
					})
				}
				if metricType == "network" {
					dimensions = append(dimensions, types.Dimension{
						Name:  aws.String("interface"),
						Value: aws.String(networkInterface),
					})
				}
				if metricType == "disk" {
					variants = []string{"/", "/tmp", "/var/lib/docker", "/home/runner"}
					dimensions = append(dimensions, types.Dimension{
						Name:  aws.String("fstype"),
						Value: aws.String("ext4"),
					})
					dimensions = append(dimensions, types.Dimension{
						Name:  aws.String("path"),
						Value: aws.String("/"),
					})
				}
				if metricType == "io" {
					dimensions = append(dimensions, types.Dimension{
						Name:  aws.String("name"),
						Value: aws.String(diskDevice),
					})
				}
				for _, variant := range variants {
					if metricType == "disk" {
						dimensions[len(dimensions)-1].Value = aws.String(variant)
					}
					summary := collector.GetMetricSummary(measurement.RealName, NAMESPACE, measurement.Aggregation, dimensions, launchTime)
					if metricType == "disk" && variant != "/" && summary == nil {
						continue
					}
					displayMetric(action, measurement.Rename, summary, measurement.Unit, formatter, variant)
				}
			}
		}
	}
}

// displayMetric shows a metric in the specified format (sparkline or chart)
func displayMetric(action *githubactions.Action, name string, summary *MetricSummary, unit string, formatter string, variant string) {
	if summary == nil {
		action.Infof("  %-12s ─────────────── (no data yet)", name)
		return
	}
	if formatter == "chart" {
		action.Infof("\n📊 %s:", name)
		caption := fmt.Sprintf("%s (%s)", name, unit)
		// Build graph options
		opts := []asciigraph.Option{
			asciigraph.Height(8),
			asciigraph.Width(60),
			asciigraph.Caption(caption),
			asciigraph.Precision(1),
		}

		// For percentage based metrics, force y-axis from 0 to 100
		if strings.EqualFold(unit, "percent") {
			opts = append(opts, asciigraph.LowerBound(0), asciigraph.UpperBound(100))
		}

		graph := asciigraph.Plot(summary.Data, opts...)
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

type MetricsCollector struct {
	cwClient   *cloudwatch.Client
	instanceID string
	action     *githubactions.Action
	cache      map[string]*MetricSummary // Add cache for memoization
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
		cache:      make(map[string]*MetricSummary), // Initialize cache
	}
}

func (mc *MetricsCollector) GetMetricSummary(metricName, namespace string, aggregation string, dimensions []types.Dimension, startTime time.Time) *MetricSummary {
	// Create cache key from parameters
	cacheKey := mc.createCacheKey(metricName, namespace, aggregation, dimensions, startTime)

	// Check cache first
	if cached, exists := mc.cache[cacheKey]; exists {
		return cached
	}

	// Not in cache, fetch the data
	data, err := mc.getMetricData(metricName, namespace, aggregation, dimensions, startTime)
	if err != nil {
		mc.action.Infof("Failed to get metric %s: %v", metricName, err)
		mc.cache[cacheKey] = nil // Cache nil result to avoid retries
		return nil
	}

	if len(data) == 0 {
		mc.cache[cacheKey] = nil // Cache nil result
		return nil
	}

	// Extract values and calculate stats
	values := make([]float64, len(data))
	for i, dp := range data {
		values[i] = dp.Value
	}

	min, max, avg := calculateStats(values)

	summary := &MetricSummary{
		Name: metricName,
		Data: values,
		Min:  min,
		Max:  max,
		Avg:  avg,
	}

	// Cache the result
	mc.cache[cacheKey] = summary
	return summary
}

// createCacheKey generates a unique cache key from the metric parameters
func (mc *MetricsCollector) createCacheKey(metricName, namespace string, aggregation string, dimensions []types.Dimension, startTime time.Time) string {
	var keyParts []string
	keyParts = append(keyParts, metricName, namespace, aggregation, startTime.Format(time.RFC3339))

	// Sort dimensions for consistent cache key
	dimStrs := make([]string, len(dimensions))
	for i, dim := range dimensions {
		dimStrs[i] = fmt.Sprintf("%s=%s", aws.ToString(dim.Name), aws.ToString(dim.Value))
	}
	sort.Strings(dimStrs)
	keyParts = append(keyParts, dimStrs...)

	return strings.Join(keyParts, "|")
}

func (mc *MetricsCollector) getMetricData(metricName, namespace string, aggregation string, dimensions []types.Dimension, startTime time.Time) ([]MetricDataPoint, error) {
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
					Stat:   aws.String(aggregation),
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
