package monitoring

import (
	"fmt"
	"os"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

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

// TODO: show direct link to Explorer interface of CloudWatch, but it's currently buggy as hell
func showLinks(action *githubactions.Action, metrics []string) {
	action.Infof("🔗 EC2 instance link (go to Monitoring tab): %s\n", GetEC2InstanceLink(action))
	action.Infof("🔗 CloudWatch link (%s namespace): %s\n", NAMESPACE, GetCloudWatchLink(action))
}

func GetEC2InstanceLink(action *githubactions.Action) string {
	region := os.Getenv("RUNS_ON_AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	instanceID := os.Getenv("RUNS_ON_INSTANCE_ID")
	if instanceID == "" {
		action.Fatalf("RUNS_ON_INSTANCE_ID is not set")
	}

	return fmt.Sprintf("https://%s.console.aws.amazon.com/ec2/home?region=%s#InstanceDetails:instanceId=%s",
		region, region, instanceID)
}

func GetCloudWatchLink(action *githubactions.Action) string {
	region := os.Getenv("RUNS_ON_AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	return fmt.Sprintf("https://%[1]s.console.aws.amazon.com/cloudwatch/home?region=%[1]s#metricsV2?graph=~()&namespace=~'%[2]s",
		region, NAMESPACE)
}
