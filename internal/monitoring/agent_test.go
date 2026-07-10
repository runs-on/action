package monitoring

import "testing"

func TestBuildCloudWatchConfigAddsVolumeIDToDiskMetrics(t *testing.T) {
	t.Parallel()

	config := buildCloudWatchConfig([]string{"disk"}, "ens5", "nvme0n1p1", "vol-0123456789abcdef0")
	diskConfig := config.Metrics.MetricsCollected["disk"].(map[string]interface{})

	if got := diskConfig["drop_device"]; got != true {
		t.Fatalf("drop_device = %v, want true", got)
	}
	dimensions, ok := diskConfig["append_dimensions"].(map[string]string)
	if !ok {
		t.Fatalf("append_dimensions has type %T, want map[string]string", diskConfig["append_dimensions"])
	}
	if got := dimensions["VolumeId"]; got != "vol-0123456789abcdef0" {
		t.Fatalf("VolumeId = %q, want %q", got, "vol-0123456789abcdef0")
	}
	if _, exists := dimensions["volume_id"]; exists {
		t.Fatal("unexpected lowercase volume_id dimension")
	}
}

func TestBuildCloudWatchConfigOmitsVolumeIDWhenUnavailable(t *testing.T) {
	t.Parallel()

	config := buildCloudWatchConfig([]string{"disk"}, "ens5", "nvme0n1p1", "")
	diskConfig := config.Metrics.MetricsCollected["disk"].(map[string]interface{})

	if _, exists := diskConfig["append_dimensions"]; exists {
		t.Fatal("append_dimensions should be omitted without a volume ID")
	}
}
