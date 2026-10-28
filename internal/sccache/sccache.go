package sccache

import (
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

const (
	// KeyPrefixRoot is the namespace RunsOn owns for compiler caches inside the
	// shared S3 cache bucket. Generated prefixes are scoped below it so that
	// repositories keep independent cache ownership, cost attribution, and
	// invalidation, even though they may share the bucket and the runner IAM role.
	KeyPrefixRoot = "cache/sccache"
	// keyPrefixSchema versions the generated layout so a future change to it can
	// be rolled out without reusing objects written under the previous scheme.
	keyPrefixSchema = "v1"
	// unknownScope keeps generated prefixes well formed when the environment
	// does not expose the identity a scope is derived from.
	unknownScope = "unknown"
)

// unsafeScopeChars matches everything that is not kept verbatim in a scope
// component, so a surprising repository or platform value cannot reshape the key.
var unsafeScopeChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// Configure sets sccache up for the job's next steps from the sccache input,
// whose one value, "s3", stores its cache straight in the RunsOn S3 cache
// bucket. remote_cache: sccache stores it through the agent instead
// (ConfigureRemoteCache).
func Configure(action *githubactions.Action, mode string) {
	if mode != "s3" {
		action.Warningf("sccache is %q, want \"s3\"; sccache is not configured. For the RunsOn agent's cache, use remote_cache: sccache.", mode)
		return
	}
	// Get required environment variables for RunsOn S3 cache
	bucket := os.Getenv("RUNS_ON_S3_BUCKET_CACHE")
	region := os.Getenv("RUNS_ON_AWS_REGION")

	if bucket == "" {
		action.Errorf("RUNS_ON_S3_BUCKET_CACHE environment variable is not set. sccache S3 backend requires this.")
		return
	}

	if region == "" {
		action.Errorf("RUNS_ON_AWS_REGION environment variable is not set. sccache S3 backend requires this.")
		return
	}

	prefix := DefaultKeyPrefix()

	// Set sccache environment variables
	envVars := map[string]string{
		"SCCACHE_GHA_ENABLED":   "false",
		"SCCACHE_BUCKET":        bucket,
		"SCCACHE_REGION":        region,
		"SCCACHE_S3_KEY_PREFIX": prefix,
		"RUSTC_WRAPPER":         "sccache",
	}

	action.Infof("Configuring sccache with S3 backend...")
	action.Infof("Using bucket: %s", bucket)
	action.Infof("Using region: %s", region)
	action.Infof("Using key prefix: %s", prefix)

	for key, value := range envVars {
		action.SetEnv(key, value)
		action.Infof("Set %s=%s", key, value)
	}

	action.Infof("sccache S3 backend configured successfully!")
}

// DefaultKeyPrefix builds the repository-scoped default prefix, for example
// "cache/sccache/123456789/linux-x64/v1". Repository identity comes first so a
// bucket lifecycle rule or a targeted invalidation can address a single
// repository, then the platform, then the layout version.
func DefaultKeyPrefix() string {
	return path.Join(KeyPrefixRoot, repositoryScope(), platformScope(), keyPrefixSchema)
}

// repositoryScope prefers the numeric repository id because it survives renames
// and transfers, and falls back to the owner/name slug when it is unavailable.
// The slug keeps owner and name as separate key components: flattening them into
// one would map distinct repositories onto the same prefix, since both halves may
// contain the separator (foo-bar/baz and foo/bar-baz would collide).
func repositoryScope() string {
	if id := sanitizeScope(os.Getenv("GITHUB_REPOSITORY_ID")); id != unknownScope {
		return id
	}
	owner, name, ok := strings.Cut(os.Getenv("GITHUB_REPOSITORY"), "/")
	if !ok {
		return sanitizeScope(owner)
	}
	return path.Join(sanitizeScope(owner), sanitizeScope(name))
}

// platformScope derives the runner platform from the Actions environment, and
// from the running binary when the action is exercised outside a runner. The
// fallback is translated into RUNNER_OS/RUNNER_ARCH spelling so that the same
// machine does not end up with two prefixes depending on which values are set.
func platformScope() string {
	osName := os.Getenv("RUNNER_OS")
	if strings.TrimSpace(osName) == "" {
		osName = runnerSpelling(runtime.GOOS)
	}
	arch := os.Getenv("RUNNER_ARCH")
	if strings.TrimSpace(arch) == "" {
		arch = runnerSpelling(runtime.GOARCH)
	}
	return sanitizeScope(osName) + "-" + sanitizeScope(arch)
}

// runnerSpelling maps Go's platform names onto the ones the runner exports.
func runnerSpelling(value string) string {
	switch value {
	case "darwin":
		return "macos"
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return value
	}
}

func sanitizeScope(value string) string {
	cleaned := unsafeScopeChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	cleaned = strings.Trim(cleaned, "-")
	// "." and ".." are resolved by path.Join and would walk the key out of the
	// cache/sccache namespace, so they never become a component of their own.
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return unknownScope
	}
	return cleaned
}
