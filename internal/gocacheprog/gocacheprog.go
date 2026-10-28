package gocacheprog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// Flag makes this action's binary serve the go command's build cache.
// PrefetchFlag makes it download the entries the job's previous run used.
// ShareTestResultsFlag makes either share test results between jobs too.
const (
	Flag                 = "--gocacheprog"
	PrefetchFlag         = "--gocacheprog-prefetch"
	ShareTestResultsFlag = "--share-test-results"
)

// Configure exports GOCACHEPROG for the job's next steps: this binary, run
// with Flag, serves the go command's build cache (Go 1.24+) through the RunsOn
// agent's key/value cache. The job's runtime token, which only actions
// receive, is registered with the agent, whose answer is a cache URL that
// stands for it: the helper finds it in RUNS_ON_REMOTE_CACHE_URL. Without
// it, the go command keeps its local build cache. shareTests also shares test
// results between jobs. Configure also starts, in the background, the
// download of the entries the job's previous run used.
func Configure(action *githubactions.Action, shareTests, magicCache bool, runtimeToken string) {
	const fallback = "the go command keeps its local build cache"
	if _, ok := magiccache.RegisterFor(action, "remote_cache: go", "kv", fallback, magicCache, runtimeToken); !ok {
		return
	}
	self, err := os.Executable()
	if err != nil {
		action.Warningf("remote_cache: go could not locate the action's binary (%v); %s.", err, fallback)
		return
	}
	// The go command splits GOCACHEPROG on spaces outside quotes.
	helper := self
	if strings.ContainsAny(helper, " \t") {
		helper = `"` + helper + `"`
	}
	command := helper + " " + Flag
	if shareTests {
		command += " " + ShareTestResultsFlag
	}
	action.SetEnv("GOCACHEPROG", command)
	action.Infof("Set GOCACHEPROG=%s: the go command's build cache uses Magic Cache in the next steps.", command)
	if err := startPrefetch(self, shareTests); err != nil {
		action.Warningf("remote_cache: go could not start prefetching what this job used last time (%v); the go command fetches entries as it asks for them.", err)
	}
}

// startPrefetch runs this binary with PrefetchFlag, detached from the step:
// the download overlaps the job's next steps. Its errors go to a log next to
// the local entries.
var startPrefetch = func(self string, shareTestResults bool) error {
	dir := OptionsFromEnv(shareTestResults).Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "prefetch.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	args := []string{PrefetchFlag}
	if shareTestResults {
		args = append(args, ShareTestResultsFlag)
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// SaveJobList saves, in the action's post step, the list of entries the job's
// go commands used.
func SaveJobList(action *githubactions.Action, shareTests bool) {
	opts := OptionsFromEnv(shareTests)
	saved, err := SaveList(opts, ListKey(os.Getenv("ACTIONS_RUNTIME_TOKEN")))
	if err != nil {
		action.Warningf("remote_cache: go could not save what this job used (%v); its next run prefetches what an earlier run used.", err)
	} else if saved > 0 {
		action.Infof("remote_cache: go saved the %d build cache entries this job used, for its next run to prefetch.", saved)
	}
}
