package gocacheprog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// namespace versions the remote entry format: a change starts from an empty
// cache.
const namespace = "v1"

// endpointFile holds the job's gocacheprog cache endpoint for the helper. Its
// path is the job's credential. The file sits next to the local layer, not in
// it, so emptying the layer keeps it.
const endpointFile = "runs-on-gocacheprog-endpoint"

// jobTemp is the directory the runner empties for each job, so the local
// layer, and with it the test results that are only kept there, never
// outlives the job on a runner that does.
func jobTemp() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// StoreEndpoint keeps the job's cache endpoint where the helper finds it, in
// a file only the runner's user reads. Exported, its credential would be in
// the environment of every later step and of everything those start.
func StoreEndpoint(endpoint string) error {
	path := filepath.Join(jobTemp(), endpointFile)
	if err := os.WriteFile(path, []byte(endpoint), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// OptionsFromEnv describes the job's cache from what its steps see: the
// endpoint the action stored. Without it, the cache stays on local disk.
func OptionsFromEnv(shareTestResults bool) Options {
	stored, _ := os.ReadFile(filepath.Join(jobTemp(), endpointFile))
	return Options{
		Dir:              filepath.Join(jobTemp(), "runs-on-gocacheprog"),
		Endpoint:         strings.TrimSpace(string(stored)),
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
