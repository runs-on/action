package mbx

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// keyPrefix keeps mbx objects under the stack's cache/ prefix, which the
// runner role may read and write and the cache lifecycle rule expires.
const keyPrefix = "cache/mbx"

var repositoryID = regexp.MustCompile(`^[0-9]+$`)

// credentialSources are the variables that make mbx sign with something other
// than the instance role, or refuse to fall back to it.
var credentialSources = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_PROFILE",
	"AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
}

// ConfigureMbx configures mbx (Mr. Boxington) to use the RunsOn S3 cache bucket
// as its remote cache. Currently only supports the "s3" backend.
//
// No credentials are exported: mbx 1.21.0 and later sign with the runner's
// instance role through IMDSv2 and renew it before it expires. Exporting a
// snapshot as AWS_* would take precedence, stop renewing, and also replace the
// instance profile for every later AWS tool in the job.
//
// Nothing is exported unless every value resolves.
func ConfigureMbx(action *githubactions.Action, backend string) error {
	if backend != "s3" {
		action.Warningf("Unsupported mbx backend: %s. Only 's3' is currently supported.", backend)
		return nil
	}

	bucket := os.Getenv("RUNS_ON_S3_BUCKET_CACHE")
	if bucket == "" {
		return fmt.Errorf("RUNS_ON_S3_BUCKET_CACHE environment variable is not set; the mbx S3 backend requires it")
	}
	region := os.Getenv("RUNS_ON_AWS_REGION")
	if region == "" {
		return fmt.Errorf("RUNS_ON_AWS_REGION environment variable is not set; the mbx S3 backend requires it")
	}
	namespace, err := Namespace()
	if err != nil {
		return err
	}

	settings := []struct{ key, value string }{
		{"MBX_REMOTE_URL", fmt.Sprintf("s3://%s/%s", bucket, keyPrefix)},
		{"MBX_REMOTE_NAMESPACE", namespace},
		{"MBX_REMOTE_S3_REGION", region},
	}

	action.Infof("Configuring mbx with S3 backend...")
	for _, setting := range settings {
		action.SetEnv(setting.key, setting.value)
		action.Infof("Set %s=%s", setting.key, setting.value)
	}

	action.Infof("mbx signs with the runner's instance role and renews it on its own; this requires mbx 1.21.0 or later.")
	for _, name := range credentialSources {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			action.Warningf(
				"%s is set, so mbx will not use the runner's instance role: it signs with that source if it can read it, or refuses the remote cache. Run `mbx doctor` to see which.",
				name,
			)
		}
	}
	action.Infof("mbx S3 backend configured successfully!")
	return nil
}

// Namespace returns the mbx remote namespace for this repository: its stable
// numeric ID, which survives renames and is never reused.
func Namespace() (string, error) {
	id := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY_ID"))
	if !repositoryID.MatchString(id) {
		return "", fmt.Errorf("GITHUB_REPOSITORY_ID %q is not a numeric repository ID; the mbx namespace is derived from it", id)
	}
	return id, nil
}
