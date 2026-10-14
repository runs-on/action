package sccache

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// logFile receives the sccache server's errors, unless the job names its own
// file in SCCACHE_ERROR_LOG.
const logFile = "runs-on-sccache.log"

// writeCheckFailed is what the sccache server logs when its storage refuses
// the write it tries at startup. The server then only reads from it.
var writeCheckFailed = []byte("storage write check failed")

// configureMagicCache starts the sccache server with Magic Cache as its
// storage, through sccache's GitHub Actions cache backend, and sets
// RUSTC_WRAPPER for the job's next steps. The backend needs the job's
// runtime token, which GitHub gives to actions only: the server gets it in
// its own environment, and no step does. The server never idles out, so
// every sccache client of the job uses it, RUSTC_WRAPPER as well as C and
// C++ compiler launchers. sccache must be installed before the action runs.
// Without Magic Cache, the token or sccache, the action warns and sets
// nothing.
func configureMagicCache(action *githubactions.Action, magicCache bool, runtimeToken string) {
	if !magicCache {
		action.Warningf("sccache: true needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); sccache is not configured.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("sccache: true needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; sccache is not configured.")
		return
	}
	bin, err := exec.LookPath("sccache")
	if err != nil {
		action.Warningf("sccache: true starts the sccache server, and sccache is not installed: install it before this action. sccache is not configured.")
		return
	}
	logPath := os.Getenv("SCCACHE_ERROR_LOG")
	if logPath == "" {
		logPath = filepath.Join(jobTemp(), logFile)
	}
	// A server that is already running keeps the storage it started with.
	_ = exec.Command(bin, "--stop-server").Run()
	if err := startServer(bin, logPath, magiccache.URL(), runtimeToken); err != nil {
		log, _ := os.ReadFile(logPath)
		action.Warningf("sccache could not start its server with Magic Cache (%v); sccache is not configured.\n%s", err, log)
		return
	}
	if log, _ := os.ReadFile(logPath); bytes.Contains(log, writeCheckFailed) {
		action.Warningf("Magic Cache refused sccache's writes, so sccache only reads from it: sccache's GitHub Actions cache mode needs RunsOn v3.5.0 or later. Details in %s.", logPath)
	}
	action.SetEnv("RUSTC_WRAPPER", "sccache")
	action.Infof("Started the sccache server with Magic Cache as its storage, and set RUSTC_WRAPPER=sccache for the next steps. Its errors go to %s.", logPath)
}

// startServer starts the sccache server, which keeps running in the
// background. The output of the command and the server's errors go to
// logPath: a file rather than a pipe, which a process left holding it would
// keep the action waiting on.
func startServer(bin, logPath, cacheURL, runtimeToken string) error {
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command(bin, "--start-server")
	cmd.Env = serverEnv(os.Environ(), logPath, cacheURL, runtimeToken)
	cmd.Stdout, cmd.Stderr = log, log
	return cmd.Run()
}

// serverEnv is the sccache server's environment: the step's, with what the
// GitHub Actions cache backend reads pointing at Magic Cache. It leaves out
// the action's inputs, the token input among them, and the other variables
// GitHub gives to actions, which the server does not need.
func serverEnv(environ []string, logPath, cacheURL, runtimeToken string) []string {
	env := make([]string, 0, len(environ)+6)
	for _, kv := range environ {
		name, _, _ := strings.Cut(strings.ToUpper(kv), "=")
		if strings.HasPrefix(name, "INPUT_") || strings.HasPrefix(name, "ACTIONS_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"SCCACHE_GHA_ENABLED=true",
		// The server serves the whole job, however long its steps run
		// between two compilations.
		"SCCACHE_IDLE_TIMEOUT=0",
		"SCCACHE_ERROR_LOG="+logPath,
		"ACTIONS_CACHE_SERVICE_V2=true",
		"ACTIONS_RESULTS_URL="+cacheURL,
		"ACTIONS_RUNTIME_TOKEN="+runtimeToken,
	)
}

// jobTemp is the directory the runner empties for each job.
func jobTemp() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}
