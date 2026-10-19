// Package gradle gives Gradle a remote build cache stored through Magic
// Cache: this binary serves Gradle's HTTP build cache protocol on the
// runner's loopback interface, and an init script points the job's builds at
// it.
package gradle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// ServeFlag makes this action's binary serve the build cache.
const ServeFlag = "--gradle-build-cache-serve"

// port is where the server listens. The init script names it, and Gradle's
// configuration cache is only reused while the init scripts are unchanged:
// a port that stays the same from one job to the next keeps a configuration
// cache that the job restores valid. When it is taken, the server listens on
// another port.
const port = 6880

const (
	// startWait bounds how long the action waits for the server to listen.
	startWait = 10 * time.Second
	// uploadWait bounds how long the post step waits for pending uploads.
	uploadWait = 5 * time.Minute
)

// Dir holds the server's state file, log and local entries. The runner
// empties it for each job.
func Dir() string {
	dir := os.Getenv("RUNNER_TEMP")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "runs-on-gradle-build-cache")
}

// state is what the server publishes once it listens.
type state struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// serverConfig reaches the server on its standard input, so the job's token
// is never in its environment, which other processes of the job can read.
type serverConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Configure starts the build cache server, unless an earlier step of the job
// did, and writes the init script that points Gradle at it. When anything is
// missing, it warns and Gradle builds without the remote cache.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	if mode != "true" {
		action.Warningf("gradle_build_cache is %q, want \"true\"; Gradle builds without the remote cache.", mode)
		return
	}
	if !magicCache {
		action.Warningf("gradle_build_cache needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); Gradle builds without the remote cache.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("gradle_build_cache needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; Gradle builds without the remote cache.")
		return
	}
	listening, err := ensureServer(magiccache.URL(), runtimeToken)
	if err != nil {
		logTail, _ := os.ReadFile(filepath.Join(Dir(), "server.log"))
		action.Warningf("gradle_build_cache could not start its server (%v); Gradle builds without the remote cache.\n%s", err, logTail)
		return
	}
	script := initScriptPath()
	if err := writeInitScript(script, listening); err != nil {
		action.Warningf("gradle_build_cache could not write %s (%v); Gradle builds without the remote cache.", script, err)
		return
	}
	action.Infof("Wrote %s: Gradle builds in the next steps use the remote build cache on port %d, stored through Magic Cache, unless they configure their own.", script, listening)
}

// ensureServer returns the port of the job's server, which it starts unless
// it already runs.
func ensureServer(cacheURL, token string) (int, error) {
	dir := Dir()
	if st, err := readState(dir); err == nil && healthy(st.Port) {
		return st.Port, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	_ = os.Remove(filepath.Join(dir, "state.json"))
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = logFile.Close() }()
	config, err := json.Marshal(serverConfig{URL: cacheURL, Token: token})
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(self, ServeFlag)
	cmd.Env = serverEnv(os.Environ())
	cmd.Stdin = strings.NewReader(string(config))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// The server outlives this step: it serves the job's builds until the
	// post step stops it.
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	for deadline := time.Now().Add(startWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if st, err := readState(dir); err == nil && healthy(st.Port) {
			return st.Port, nil
		}
	}
	return 0, fmt.Errorf("the server did not listen within %s", startWait)
}

// serverEnv leaves out the action's inputs, the token input among them, and
// the variables GitHub gives to actions: the server needs none of them.
func serverEnv(environ []string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(strings.ToUpper(kv), "=")
		if strings.HasPrefix(name, "INPUT_") || strings.HasPrefix(name, "ACTIONS_") || name == "GITHUB_TOKEN" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func readState(dir string) (state, error) {
	var st state
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if st.Port <= 0 {
		return st, fmt.Errorf("invalid port %d", st.Port)
	}
	return st, nil
}

func healthy(port int) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath))
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return resp.StatusCode == http.StatusOK && string(body) == healthBody
}

// initScriptPath is in Gradle's user home, whose init.d scripts every build
// runs.
func initScriptPath() string {
	home := os.Getenv("GRADLE_USER_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, ".gradle")
	}
	return filepath.Join(home, "init.d", "runs-on-build-cache.gradle")
}

// initScript turns the build cache on and makes the server the build's
// remote cache, unless the build configures an enabled remote cache of its
// own. Included builds run it too, and Gradle ignores their configuration in
// favor of the root build's, except for builds that provide plugins.
const initScript = `// Written by runs-on/action (gradle_build_cache) for this job: a remote
// build cache stored through RunsOn Magic Cache.
gradle.startParameter.buildCacheEnabled = true
gradle.settingsEvaluated { settings ->
    def remote = settings.buildCache.remote
    if (remote != null && remote.enabled) {
        logger.lifecycle("runs-on/action: this build configures its own remote build cache, which it uses instead of Magic Cache.")
        return
    }
    // A remote cache of the same type is configured in place, so a disabled
    // one must be turned on.
    settings.buildCache.remote(HttpBuildCache) {
        enabled = true
        url = 'http://127.0.0.1:%d/cache/'
        push = true
        allowInsecureProtocol = true
    }
}
`

func writeInitScript(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, fmt.Appendf(nil, initScript, port), 0o644)
}

// Finish runs in the action's post step: it waits for the server's pending
// uploads, stops it, reports what it did, and removes the init script, which
// would otherwise point later builds on the machine at a stopped server.
func Finish(action *githubactions.Action) {
	st, err := readState(Dir())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	_ = os.Remove(initScriptPath())
	if err != nil {
		action.Warningf("gradle_build_cache could not find its server (%v).", err)
		return
	}
	stats, err := stop(st.Port, uploadWait)
	if err != nil {
		action.Warningf("gradle_build_cache could not stop its server (%v); entries it had not uploaded are lost.", err)
		return
	}
	_ = os.Remove(filepath.Join(Dir(), "state.json"))
	if data, err := json.Marshal(stats); err == nil {
		_ = os.WriteFile(filepath.Join(Dir(), "stats.json"), data, 0o644)
	}
	action.Infof("gradle_build_cache: Gradle loaded %d entries from Magic Cache (%s, waiting %s in all) and %d stored earlier in the job; %d were not in the cache.",
		stats.RemoteHits, mib(stats.DownloadedBytes), stats.RemoteWait.Round(time.Millisecond), stats.LocalHits, stats.Misses)
	action.Infof("gradle_build_cache: Gradle stored %d entries: %d uploaded to Magic Cache (%s), %d already there.",
		stats.Stores, stats.Uploaded, mib(stats.UploadedBytes), stats.AlreadyStored)
	if stats.Pending > 0 || stats.RemoteErrors > 0 {
		logTail, _ := os.ReadFile(filepath.Join(Dir(), "server.log"))
		action.Warningf("gradle_build_cache: %d entries were not uploaded within %s, and %d Magic Cache calls failed.\n%s", stats.Pending, uploadWait, stats.RemoteErrors, logTail)
	}
}

func mib(bytes int64) string {
	return strconv.FormatFloat(float64(bytes)/(1<<20), 'f', 1, 64) + " MiB"
}

func stop(port int, wait time.Duration) (Stats, error) {
	var stats Stats
	client := http.Client{Timeout: wait + 30*time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d%s?wait=%s", port, stopPath, wait), "", nil)
	if err != nil {
		return stats, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return stats, fmt.Errorf("the server answered %s", resp.Status)
	}
	return stats, json.NewDecoder(resp.Body).Decode(&stats)
}

// Serve runs the server until the action stops it. It reads the Magic Cache
// URL and the job's token from in.
func Serve(in io.Reader) error {
	var config serverConfig
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&config); err != nil {
		return fmt.Errorf("read the server's configuration: %w", err)
	}
	var remote *magiccache.Client
	if config.URL != "" && config.Token != "" {
		remote = magiccache.NewClient(config.URL, config.Token)
	}
	dir := Dir()
	server, err := NewServer(dir, remote, log.New(os.Stderr, "", log.LstdFlags))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	st := state{Port: listener.Addr().(*net.TCPAddr).Port, PID: os.Getpid()}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "state.json")); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	select {
	case err := <-errs:
		return err
	case <-signals:
	case <-server.Stopped():
	}
	// Shutdown lets the stop request get its answer.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}
