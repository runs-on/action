package gocacheprog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// namespace versions the remote entry format: a change starts from an empty
// cache.
const namespace = "runs-on-gocacheprog-v1"

// OptionsFromEnv describes the job's cache from what its steps see. The job's
// ACTIONS_RUNTIME_TOKEN, which Configure exports, and the cache service URL
// reach Magic Cache; without them, the cache stays on local disk. Steps see
// the URL as ACTIONS_RESULTS_URL, or on custom runners, which give it to
// actions only, as RUNS_ON_MAGIC_CACHE_URL.
func OptionsFromEnv(shareTestResults bool) Options {
	resultsURL := os.Getenv("ACTIONS_RESULTS_URL")
	if resultsURL == "" {
		resultsURL = os.Getenv("RUNS_ON_MAGIC_CACHE_URL")
	}
	// The runner empties RUNNER_TEMP for each job, so the local layer, and
	// with it the test results that are only kept there, never outlives the
	// job on a runner that does.
	base := os.Getenv("RUNNER_TEMP")
	if base == "" {
		base = os.TempDir()
	}
	return Options{
		Dir:              filepath.Join(base, "runs-on-gocacheprog"),
		ResultsURL:       resultsURL,
		Token:            os.Getenv("ACTIONS_RUNTIME_TOKEN"),
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
