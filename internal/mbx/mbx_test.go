package mbx

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// TestMain lets the test binary act as the helper that Configure starts.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Flag {
		if err := Serve(context.Background(), os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fileCommands reads what the action wrote to GITHUB_ENV or GITHUB_STATE.
func fileCommands(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		name, delimiter, ok := strings.Cut(scanner.Text(), "<<")
		if !ok {
			continue
		}
		var lines []string
		for scanner.Scan() && scanner.Text() != delimiter {
			lines = append(lines, scanner.Text())
		}
		values[name] = strings.Join(lines, "\n")
	}
	return values
}

func setActionEnv(t *testing.T) (envFile, stateFile string) {
	t.Helper()
	dir := t.TempDir()
	envFile, stateFile = filepath.Join(dir, "env"), filepath.Join(dir, "state")
	t.Setenv("GITHUB_ENV", envFile)
	t.Setenv("GITHUB_STATE", stateFile)
	t.Setenv("RUNNER_TEMP", t.TempDir())
	t.Setenv("GITHUB_REPOSITORY_ID", "123456")
	t.Setenv("MBX_REMOTE_NAMESPACE", "")
	t.Setenv("MBX_REMOTE_TOKEN", "")
	t.Setenv("RUNS_ON_MAGIC_CACHE_URL", "")
	return envFile, stateFile
}

func TestConfigureWarnsAndExportsNothingWithoutMagicCacheOrToken(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		magicCache bool
		token      string
		warning    string
	}{
		{"unknown mode", "s3", true, testRuntimeToken, `mbx is "s3", want "true"`},
		{"no Magic Cache", "true", false, testRuntimeToken, "needs Magic Cache"},
		{"no token", "true", true, "", "needs the job's ACTIONS_RUNTIME_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envFile, _ := setActionEnv(t)
			var log bytes.Buffer
			Configure(githubactions.New(githubactions.WithWriter(&log)), tc.mode, tc.magicCache, tc.token)
			if !strings.Contains(log.String(), tc.warning) {
				t.Errorf("log %q does not warn %q", log.String(), tc.warning)
			}
			if env := fileCommands(t, envFile); len(env) != 0 {
				t.Errorf("exported %v", env)
			}
		})
	}
}

func TestConfigureStartsTheHelperAndPostStopsIt(t *testing.T) {
	cacheSrv, cache := newFakeCacheService(t)
	envFile, stateFile := setActionEnv(t)
	t.Setenv("ACTIONS_RESULTS_URL", cacheSrv.URL)
	t.Setenv("INPUT_TOKEN", "github-token")

	var log bytes.Buffer
	action := githubactions.New(githubactions.WithWriter(&log))
	Configure(action, "true", true, testRuntimeToken)
	env := fileCommands(t, envFile)
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/$`).MatchString(env["MBX_REMOTE_URL"]) || env["MBX_REMOTE_TOKEN"] == "" || env["MBX_REMOTE_NAMESPACE"] != "123456" {
		t.Fatalf("exported %v\n%s", env, log.String())
	}
	if !strings.Contains(log.String(), "::add-mask::"+env["MBX_REMOTE_TOKEN"]) {
		t.Errorf("the client token is not masked")
	}
	state, err := readState(filepath.Join(os.Getenv("RUNNER_TEMP"), stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stopHelper(state.PID) })

	// The helper holds the runtime token, not in its environment, and none
	// of the action's inputs.
	if runtime.GOOS == "linux" {
		environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", state.PID))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(environ, []byte(testRuntimeToken)) || bytes.Contains(environ, []byte("INPUT_TOKEN")) {
			t.Errorf("the helper's environment holds the runtime token or the action's inputs")
		}
	}

	// mbx stores and reads an object through the helper.
	url := env["MBX_REMOTE_URL"] + "v1/blobs/blake3/" + hash('a') + "/5"
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		var body *strings.Reader
		if method == http.MethodPut {
			body = strings.NewReader("hello")
		} else {
			body = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, url, body)
		req.Header.Set("Authorization", "Bearer "+env["MBX_REMOTE_TOKEN"])
		req.Header.Set(namespaceHeader, env["MBX_REMOTE_NAMESPACE"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s: status %d", method, resp.StatusCode)
		}
	}
	if cache.entryCount() != 1 {
		t.Errorf("%d cache entries, want 1", cache.entryCount())
	}

	// Another invocation in the same job reuses the helper.
	t.Setenv("MBX_REMOTE_TOKEN", env["MBX_REMOTE_TOKEN"])
	log.Reset()
	Configure(action, "true", true, testRuntimeToken)
	if !strings.Contains(log.String(), "already serves") {
		t.Errorf("second Configure: %s", log.String())
	}

	for name, value := range fileCommands(t, stateFile) {
		t.Setenv("STATE_"+name, value)
	}

	// An upload still in flight when the post step starts completes before
	// the helper exits.
	body, upload := io.Pipe()
	req, _ := http.NewRequest(http.MethodPut, env["MBX_REMOTE_URL"]+"v1/blobs/blake3/"+hash('b')+"/10", body)
	req.ContentLength = 10
	req.Header.Set("Authorization", "Bearer "+env["MBX_REMOTE_TOKEN"])
	req.Header.Set(namespaceHeader, env["MBX_REMOTE_NAMESPACE"])
	uploaded := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			uploaded <- 0
			return
		}
		_ = resp.Body.Close()
		uploaded <- resp.StatusCode
	}()
	_, _ = upload.Write([]byte("hello"))
	time.Sleep(200 * time.Millisecond)

	log.Reset()
	posted := make(chan struct{})
	go func() {
		Post(action)
		close(posted)
	}()
	select {
	case <-posted:
		t.Fatal("the post step returned while an upload was in flight")
	case <-time.After(500 * time.Millisecond):
	}
	_, _ = upload.Write([]byte("world"))
	_ = upload.Close()
	if status := <-uploaded; status != http.StatusCreated {
		t.Fatalf("the upload in flight got status %d, want 201", status)
	}
	select {
	case <-posted:
	case <-time.After(10 * time.Second):
		t.Fatal("the post step did not return")
	}
	if !strings.Contains(log.String(), "1 hits, 0 misses, 1 uploads") {
		t.Errorf("post: %s", log.String())
	}
	if cache.entryCount() != 2 {
		t.Errorf("%d cache entries, want 2", cache.entryCount())
	}
	if healthy(state.Port, env["MBX_REMOTE_TOKEN"]) {
		t.Error("the helper still answers after the post step")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("RUNNER_TEMP"), stateFileName)); !os.IsNotExist(err) {
		t.Errorf("the helper's state file is still there: %v", err)
	}
}
