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
const namespace = "runs-on-gocacheprog-v1"

// tokenFile holds the job's runtime token for the helper. It sits next to the
// local layer, not in it, so emptying the layer keeps the token.
const tokenFile = "runs-on-gocacheprog-token"

// jobTemp is the directory the runner empties for each job, so the local
// layer, and with it the test results that are only kept there, never
// outlives the job on a runner that does.
func jobTemp() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// StoreToken keeps the job's runtime token where the helper finds it, in a
// file only the runner's user reads. GitHub gives the token to actions, not
// to run steps: exported, it would be in the environment of every later step
// and of everything those start.
func StoreToken(token string) error {
	path := filepath.Join(jobTemp(), tokenFile)
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// OptionsFromEnv describes the job's cache from what its steps see. The job's
// runtime token and the cache service URL reach Magic Cache; without them,
// the cache stays on local disk. Actions have the token in their environment;
// the helper, started from a run step, reads the copy StoreToken kept. The
// action, its prefetch and the helper all use the service magiccache.URL
// names.
func OptionsFromEnv(shareTestResults bool) Options {
	token := os.Getenv("ACTIONS_RUNTIME_TOKEN")
	if token == "" {
		stored, _ := os.ReadFile(filepath.Join(jobTemp(), tokenFile))
		token = strings.TrimSpace(string(stored))
	}
	return Options{
		Dir:              filepath.Join(jobTemp(), "runs-on-gocacheprog"),
		ResultsURL:       magiccache.URL(),
		Token:            token,
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
