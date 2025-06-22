package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/sethvargo/go-githubactions"
)

// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-Agent-Configuration-File-Details.html
func GenerateCloudWatchConfig(action *githubactions.Action, metrics []string, networkInterface, diskDevice string) error {
	if len(metrics) == 0 {
		return nil
	}

	// Enable detailed monitoring for the instance
	if err := enableDetailedMonitoring(action); err != nil {
		action.Warningf("Failed to enable detailed monitoring: %v", err)
	}

	// Get network interface and disk device based on config
	primaryInterface := getNetworkInterface(networkInterface)
	rootDisk := getDiskDevice(diskDevice)

	action.Infof("Using network interface: %s", primaryInterface)
	action.Infof("Using disk device: %s", rootDisk)

	config := CloudWatchConfig{
		Metrics: MetricsConfig{
			Namespace:        NAMESPACE,
			MetricsCollected: make(map[string]interface{}),
			AppendDimensions: map[string]string{
				"InstanceId": "${aws:InstanceId}",
			},
			ForceFlushInterval: 5, // 5 seconds
		},
		Agent: AgentConfig{
			MetricsCollectionInterval: 10,
		},
	}

	// Configure metrics based on input with more frequent collection for detailed monitoring
	for _, metric := range metrics {
		measurements := GetMeasurements(metric)
		switch strings.ToLower(metric) {
		case "cpu":
			cpuConfig := map[string]interface{}{
				"drop_original_metrics": true,
				"measurement":           []string{},
				"totalcpu":              true,
			}
			for _, measurement := range measurements {
				cpuConfig["measurement"] = append(cpuConfig["measurement"].([]string), measurement.Name)
			}
			config.Metrics.MetricsCollected["cpu"] = cpuConfig
		case "network":
			netConfig := map[string]interface{}{
				"drop_original_metrics": true,
				"measurement":           []string{},
				"resources":             []string{primaryInterface},
			}
			for _, measurement := range measurements {
				netConfig["measurement"] = append(netConfig["measurement"].([]string), measurement.Name)
			}
			config.Metrics.MetricsCollected["net"] = netConfig
		case "memory":
			memConfig := map[string]interface{}{
				"drop_original_metrics": true,
				"measurement":           []string{},
			}
			for _, measurement := range measurements {
				memConfig["measurement"] = append(memConfig["measurement"].([]string), measurement.Name)
			}
			config.Metrics.MetricsCollected["mem"] = memConfig
		case "disk":
			diskConfig := map[string]interface{}{
				"drop_original_metrics": true,
				"drop_device":           true,
				"measurement":           []string{},
				"resources":             []string{"/", "/tmp", "/var/lib/docker", "/home/runner"},
				"ignore_file_system_types": []string{
					"sysfs", "devtmpfs",
				},
			}
			for _, measurement := range measurements {
				diskConfig["measurement"] = append(diskConfig["measurement"].([]string), measurement.Name)
			}
			config.Metrics.MetricsCollected["disk"] = diskConfig
		case "io":
			diskioConfig := map[string]interface{}{
				"drop_original_metrics": true,
				"measurement":           []string{},
				"resources":             []string{rootDisk},
			}
			for _, measurement := range measurements {
				diskioConfig["measurement"] = append(diskioConfig["measurement"].([]string), measurement.Name)
			}
			config.Metrics.MetricsCollected["diskio"] = diskioConfig
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
	action.Infof("Config content: %s", string(configJSON))

	// Append the config to the running CloudWatch agent
	return configStartCloudWatchAgent(action, configPath)
}

func configStartCloudWatchAgent(action *githubactions.Action, configPath string) error {
	// Check if CloudWatch agent installation exists
	agentBinDir := "/opt/aws/amazon-cloudwatch-agent/bin/"
	if _, err := os.Stat(agentBinDir); os.IsNotExist(err) {
		action.Warningf("CloudWatch agent bin directory not found at %s, skipping metrics configuration", agentBinDir)
		return nil
	}

	// Check if start script is available
	agentStarter := "/opt/aws/amazon-cloudwatch-agent/bin/start-amazon-cloudwatch-agent"
	if _, err := os.Stat(agentStarter); os.IsNotExist(err) {
		action.Warningf("CloudWatch agent start script not found at %s, skipping metrics configuration", agentStarter)
		return nil
	}

	// Create etc directory if it doesn't exist
	etcDir := "/opt/aws/amazon-cloudwatch-agent/etc"
	if err := exec.Command("sudo", "mkdir", "-p", etcDir).Run(); err != nil {
		action.Warningf("Failed to create etc directory %s: %v", etcDir, err)
		return err
	}

	// Read the generated config
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Store the configuration directly in the standard CloudWatch agent location
	configDestination := "/opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json"
	
	// Write config directly to the standard location
	if err := os.WriteFile(configDestination, configData, 0644); err != nil {
		action.Warningf("Failed to write config to %s: %v", configDestination, err)
		return err
	}

	action.Infof("Created CloudWatch config at %s", configDestination)

	// Start the CloudWatch agent in fire-and-forget mode
	action.Infof("Starting Amazon CloudWatch agent in background...")
	cmd := exec.Command("nohup", "sudo", agentStarter)
	cmd.Stdout = nil
	cmd.Stderr = nil
	
	if err := cmd.Start(); err != nil {
		action.Warningf("Failed to start CloudWatch agent: %v", err)
		return err
	}

	action.Infof("CloudWatch agent start script launched, waiting 3 seconds for agent to initialize...")
	
	// Wait for agent to initialize
	exec.Command("sleep", "3").Run()

	// Check if agent process is running
	checkCmd := exec.Command("pgrep", "-f", "amazon-cloudwatch-agent")
	if output, err := checkCmd.Output(); err == nil && len(output) > 0 {
		action.Infof("✅ Amazon CloudWatch agent processes found: %s", strings.TrimSpace(string(output)))
	} else {
		action.Warningf("No Amazon CloudWatch agent processes found after startup")
	}

	return nil
}

func enableDetailedMonitoring(action *githubactions.Action) error {
	return nil
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
