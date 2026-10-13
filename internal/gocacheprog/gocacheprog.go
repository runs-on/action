package gocacheprog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// The values of the gocacheprog input.
const (
	modeBuilds    = "true"
	modeWithTests = "with-tests"
)

// Configure exports GOCACHEPROG for the job's next steps: this binary, run
// with Flag, serves the go command's build cache (Go 1.24+) through Magic
// Cache. The helper needs the job's runtime token, which only actions
// receive: Configure keeps it in a file for the helper instead of exporting
// it. Without Magic Cache or a token, the go command keeps its local build
// cache. It also starts, in the background, the download of the entries the
// job's previous run used.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	if mode != modeBuilds && mode != modeWithTests {
		action.Warningf("gocacheprog is %q, want %q or %q; the go command keeps its local build cache.", mode, modeBuilds, modeWithTests)
		return
	}
	if !magicCache {
		action.Warningf("gocacheprog needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); the go command keeps its local build cache.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("gocacheprog needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; the go command keeps its local build cache.")
		return
	}
	self, err := os.Executable()
	if err != nil {
		action.Warningf("gocacheprog could not locate the action's binary (%v); the go command keeps its local build cache.", err)
		return
	}
	// The go command splits GOCACHEPROG on spaces outside quotes.
	helper := self
	if strings.ContainsAny(helper, " \t") {
		helper = `"` + helper + `"`
	}
	command := helper + " " + Flag
	if mode == modeWithTests {
		command += " " + ShareTestResultsFlag
	}
	if err := StoreToken(runtimeToken); err != nil {
		action.Warningf("gocacheprog could not keep the job's token for its helper (%v); the go command keeps its local build cache.", err)
		return
	}
	action.SetEnv("GOCACHEPROG", command)
	action.Infof("Set GOCACHEPROG=%s: the go command's build cache uses Magic Cache in the next steps.", command)
	if err := startPrefetch(self, mode == modeWithTests); err != nil {
		action.Warningf("gocacheprog could not start prefetching what this job used last time (%v); the go command fetches entries as it asks for them.", err)
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
func SaveJobList(action *githubactions.Action, mode string) {
	opts := OptionsFromEnv(mode == modeWithTests)
	saved, err := SaveList(opts, ListKey(opts.Token))
	if err != nil {
		action.Warningf("gocacheprog could not save what this job used (%v); its next run prefetches what an earlier run used.", err)
	} else if saved > 0 {
		action.Infof("gocacheprog: saved the %d build cache entries this job used, for its next run to prefetch.", saved)
	}
}
