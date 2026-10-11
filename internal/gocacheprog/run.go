package gocacheprog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// namespace versions the remote entry format: a change starts from an empty
// cache.
const namespace = "runs-on-gocacheprog-v1"

// Run serves the go command on in and out until it exits. The job's
// ACTIONS_RUNTIME_TOKEN, which Configure exports, and the cache service URL
// reach Magic Cache; without them, the cache stays on local disk. Steps see
// the URL as ACTIONS_RESULTS_URL, or on custom runners, which give it to
// actions only, as RUNS_ON_MAGIC_CACHE_URL.
func Run(in io.Reader, out io.Writer, shareTestResults bool) error {
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
	c, err := New(Options{
		Dir:         filepath.Join(base, "runs-on-gocacheprog"),
		ResultsURL:  resultsURL,
		Token:       os.Getenv("ACTIONS_RUNTIME_TOKEN"),
		Namespace:   namespace,
		ManifestKey: namespace + "-manifest-" + commandID(),

		ShareTestResults: shareTestResults,
	})
	if err != nil {
		return fmt.Errorf("open the build cache: %w", err)
	}
	return Serve(in, out, c)
}

// commandID identifies the go command that started this helper, so the next
// run of the same command in the same directory prefetches what this one
// used: go build and go test use different entries. The parent's command line
// is only readable on Linux; elsewhere the directory alone identifies it.
func commandID() string {
	h := sha256.New()
	dir, _ := os.Getwd()
	h.Write([]byte(dir))
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", os.Getppid())); err == nil {
		h.Write([]byte{0})
		h.Write(cmdline)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
