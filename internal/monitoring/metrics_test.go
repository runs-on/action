package monitoring

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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

func TestDiskMetricDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		volumeID string
		want     map[string]string
	}{
		{
			name:     "with volume ID",
			volumeID: "vol-0123456789abcdef0",
			want: map[string]string{
				"fstype":   "ext4",
				"path":     "/tmp",
				"VolumeId": "vol-0123456789abcdef0",
			},
		},
		{
			name: "without volume ID",
			want: map[string]string{
				"fstype": "ext4",
				"path":   "/tmp",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := make(map[string]string)
			for _, dimension := range diskMetricDimensions("/tmp", tt.volumeID) {
				got[aws.ToString(dimension.Name)] = aws.ToString(dimension.Value)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d dimensions, want %d: %#v", len(got), len(tt.want), got)
			}
			for name, wantValue := range tt.want {
				if gotValue := got[name]; gotValue != wantValue {
					t.Fatalf("dimension %s = %q, want %q", name, gotValue, wantValue)
				}
			}
		})
	}
}

func TestSavedVolumeID(t *testing.T) {
	t.Setenv("STATE_"+volumeIDStateKey, "vol-0123456789abcdef0")

	if got := savedVolumeID(); got != "vol-0123456789abcdef0" {
		t.Fatalf("savedVolumeID() = %q, want %q", got, "vol-0123456789abcdef0")
	}
}
