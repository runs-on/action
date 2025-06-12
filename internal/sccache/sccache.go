package sccache

import (
	"os"

	"github.com/sethvargo/go-githubactions"
)

// ConfigureSccache configures sccache with the appropriate backend.
// Currently only supports "s3" backend for RunsOn S3 cache bucket.
func ConfigureSccache(action *githubactions.Action, backend string) error {
	if backend != "s3" {
		action.Warningf("Unsupported sccache backend: %s. Only 's3' is currently supported.", backend)
		return nil
	}

	// Get required environment variables for RunsOn S3 cache
	bucket := os.Getenv("RUNS_ON_S3_BUCKET_CACHE")
	region := os.Getenv("RUNS_ON_AWS_REGION")

	if bucket == "" {
		action.Errorf("RUNS_ON_S3_BUCKET_CACHE environment variable is not set. sccache S3 backend requires this.")
		return nil
	}

	if region == "" {
		action.Errorf("RUNS_ON_AWS_REGION environment variable is not set. sccache S3 backend requires this.")
		return nil
	}

	// Set sccache environment variables
	envVars := map[string]string{
		"SCCACHE_GHA_ENABLED":   "false",
		"SCCACHE_BUCKET":        bucket,
		"SCCACHE_REGION":        region,
		"SCCACHE_S3_KEY_PREFIX": "cache/sccache",
		"RUSTC_WRAPPER":         "sccache",
	}

	action.Infof("Configuring sccache with S3 backend...")
	action.Infof("Using bucket: %s", bucket)
	action.Infof("Using region: %s", region)

	for key, value := range envVars {
		action.SetEnv(key, value)
		action.Infof("Set %s=%s", key, value)
	}

	action.Infof("sccache S3 backend configured successfully!")
	return nil
}
