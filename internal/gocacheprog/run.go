package gocacheprog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
)

// namespace versions the remote entry format: a change starts from an empty
// cache.
const namespace = "v1"

// jobTemp is the directory the runner empties for each job, so the local
// layer, and with it the test results that are only kept there, never
// outlives the job on a runner that does.
func jobTemp() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// OptionsFromEnv describes the job's cache from what its steps see: the
// gocacheprog namespace of the key/value cache the action registered, in
// RUNS_ON_TOOLS_KV_URL. Without it, the cache stays on local disk.
func OptionsFromEnv(shareTestResults bool) Options {
	endpoint := ""
	if base := strings.TrimSpace(os.Getenv(magiccache.ToolsKVURLEnv)); base != "" {
		endpoint = strings.TrimSuffix(base, "/") + "/gocacheprog"
	}
	return Options{
		Dir:              filepath.Join(jobTemp(), "runs-on-gocacheprog"),
		Endpoint:         endpoint,
		Namespace:        namespace,
		ShareTestResults: shareTestResults,
	}
}

// Run serves the go command on in and out until it exits.
func Run(in io.Reader, out io.Writer, shareTestResults bool) error {
	c, err := New(OptionsFromEnv(shareTestResults))
	if err != nil {
		return fmt.Errorf("open the build cache: %w", err)
	}
	return Serve(in, out, c)
}
