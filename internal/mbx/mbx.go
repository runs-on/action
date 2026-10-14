// Package mbx gives mbx (Mr. Boxington) a remote cache in Magic Cache. The
// action starts a helper that serves mbx's remote cache protocol on the
// loopback interface and keeps every object as a GitHub Actions cache entry,
// so the job's cache scopes and the stack's cache isolation apply.
package mbx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

const (
	stateFileName = "runs-on-mbx.json"
	logFileName   = "runs-on-mbx.log"
	// startWait bounds how long the action waits for the helper to listen.
	startWait = 15 * time.Second
	// stateStarted tells the post step of the invocation that started the
	// helper to stop it.
	stateStarted = "mbx_helper_started"
	stateToken   = "mbx_helper_token"
)

// jobTemp is the directory the runner empties for each job.
func jobTemp() string {
	if dir := os.Getenv("RUNNER_TEMP"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// resultsURL is Magic Cache's GitHub Actions cache service. Custom runners
// name it RUNS_ON_MAGIC_CACHE_URL; the others point ACTIONS_RESULTS_URL at
// it.
func resultsURL() string {
	if url := os.Getenv("RUNS_ON_MAGIC_CACHE_URL"); url != "" {
		return url
	}
	return os.Getenv("ACTIONS_RESULTS_URL")
}

// Configure starts the helper and points mbx at it for the job's next steps,
// with MBX_REMOTE_URL, MBX_REMOTE_TOKEN and, unless the job set one,
// MBX_REMOTE_NAMESPACE. Without Magic Cache or the job's runtime token, it
// warns and sets nothing: mbx then keeps its local cache.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	if mode != "true" {
		action.Warningf("mbx is %q, want \"true\"; mbx is not configured.", mode)
		return
	}
	if !magicCache {
		action.Warningf("mbx: true needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); mbx is not configured.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("mbx: true needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; mbx is not configured.")
		return
	}
	stateFile := filepath.Join(jobTemp(), stateFileName)
	if state, err := readState(stateFile); err == nil && healthy(state.Port, os.Getenv("MBX_REMOTE_TOKEN")) {
		action.Infof("mbx: an earlier step already serves mbx's remote cache on 127.0.0.1:%d.", state.Port)
		return
	}
	state, clientToken, err := start(runtimeToken, stateFile)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(jobTemp(), logFileName))
		action.Warningf("mbx: the helper did not start (%v); mbx is not configured.\n%s", err, log)
		return
	}
	action.AddMask(clientToken)
	action.SaveState(stateStarted, "true")
	action.SaveState(stateToken, clientToken)
	action.SetEnv("MBX_REMOTE_URL", fmt.Sprintf("http://127.0.0.1:%d/", state.Port))
	action.SetEnv("MBX_REMOTE_TOKEN", clientToken)
	if os.Getenv("MBX_REMOTE_NAMESPACE") == "" {
		action.SetEnv("MBX_REMOTE_NAMESPACE", defaultNamespace())
	}
	action.Infof("mbx: serving mbx's remote cache from Magic Cache on 127.0.0.1:%d, with this job's cache scopes. Install mbx after this step, for instance with jdx/mr-boxington-action and backend: remote. The helper's errors go to %s.", state.Port, filepath.Join(jobTemp(), logFileName))
}

// start runs this binary as the helper, detached so it outlives the step, and
// waits until it answers.
func start(runtimeToken, stateFile string) (State, string, error) {
	self, err := os.Executable()
	if err != nil {
		return State{}, "", fmt.Errorf("locate the action binary: %w", err)
	}
	clientToken, err := randomToken()
	if err != nil {
		return State{}, "", err
	}
	logFile, err := os.OpenFile(filepath.Join(jobTemp(), logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return State{}, "", err
	}
	defer func() { _ = logFile.Close() }()
	_ = os.Remove(stateFile)
	pid, err := startDetached(func() *exec.Cmd {
		cmd := exec.Command(self, Flag)
		cmd.Env = append(helperEnviron(os.Environ()), envStateFile+"="+stateFile, envResultsURL+"="+resultsURL())
		cmd.Stdin = strings.NewReader(runtimeToken + "\n" + clientToken + "\n")
		cmd.Stdout, cmd.Stderr = logFile, logFile
		return cmd
	})
	if err != nil {
		return State{}, "", err
	}
	deadline := time.Now().Add(startWait)
	for {
		if state, err := readState(stateFile); err == nil && state.PID == pid && healthy(state.Port, clientToken) {
			return state, clientToken, nil
		}
		if time.Now().After(deadline) {
			return State{}, "", fmt.Errorf("it did not answer within %s", startWait)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// helperEnviron is the helper's environment: the step's, without the action's
// inputs and the variables GitHub gives to actions, which hold credentials it
// does not need.
func helperEnviron(environ []string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(strings.ToUpper(kv), "=")
		if strings.HasPrefix(name, "INPUT_") || strings.HasPrefix(name, "ACTIONS_") || strings.HasPrefix(name, "RUNS_ON_MBX_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// defaultNamespace keeps the repository's objects apart: its ID survives a
// rename.
func defaultNamespace() string {
	if id := os.Getenv("GITHUB_REPOSITORY_ID"); id != "" {
		return id
	}
	return os.Getenv("GITHUB_REPOSITORY")
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func healthy(port int, clientToken string) bool {
	if clientToken == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1/capabilities", port), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+clientToken)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Post prints what the helper served and stops it, in the post step of the
// invocation that started it.
func Post(action *githubactions.Action) {
	if os.Getenv("STATE_"+stateStarted) != "true" {
		return
	}
	stateFile := filepath.Join(jobTemp(), stateFileName)
	state, err := readState(stateFile)
	if err != nil {
		return
	}
	if stats, err := fetchStats(state.Port, os.Getenv("STATE_"+stateToken)); err == nil {
		action.Infof("mbx remote cache in Magic Cache: %d hits, %d misses, %d uploads (%d already stored), %d manifest updates; %s downloaded, %s uploaded; %d errors.",
			stats.Hits, stats.Misses, stats.Uploads, stats.Existing, stats.Manifests, formatBytes(stats.BytesDown), formatBytes(stats.BytesUp), stats.Errors)
		if stats.Refused > 0 {
			action.Warningf("mbx: Magic Cache refused %d requests for this job's cache token. A job whose token only reads (cache-mode: read, or a trigger GitHub treats as low-trust) cannot publish.", stats.Refused)
		}
	}
	if err := stopHelper(state.PID); err != nil {
		action.Warningf("mbx: could not stop the helper (PID %d): %v", state.PID, err)
	}
	_ = os.Remove(stateFile)
}

func fetchStats(port int, clientToken string) (Stats, error) {
	var stats Stats
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, StatsPath), nil)
	if err != nil {
		return stats, err
	}
	req.Header.Set("Authorization", "Bearer "+clientToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return stats, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return stats, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return stats, json.NewDecoder(resp.Body).Decode(&stats)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " B"
}
