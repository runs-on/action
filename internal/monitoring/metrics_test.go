package monitoring

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestDisplayMetricChartSkipsInvalidValues(t *testing.T) {
	var output bytes.Buffer
	action := githubactions.New(githubactions.WithWriter(&output))

	displayMetric(action, "CPU System", &MetricSummary{
		Data: []float64{0, math.NaN(), 50, math.Inf(1), 100},
	}, "Percent", "chart", "default")

	got := output.String()
	if !strings.Contains(got, "CPU System") {
		t.Fatalf("expected metric name in output, got %q", got)
	}
	if !strings.Contains(got, "Stats: min:0.0 avg:50.0 max:100.0 Percent") {
		t.Fatalf("expected sanitized stats in output, got %q", got)
	}
}

func TestDisplayMetricAllInvalidValuesShowsNoValidData(t *testing.T) {
	var output bytes.Buffer
	action := githubactions.New(githubactions.WithWriter(&output))

	displayMetric(action, "CPU System", &MetricSummary{
		Data: []float64{math.NaN(), math.Inf(1), math.Inf(-1)},
	}, "Percent", "chart", "default")

	if !strings.Contains(output.String(), "(no valid data yet)") {
		t.Fatalf("expected no-valid-data message, got %q", output.String())
	}
}
