// Package bazel points Bazel at the remote cache that the RunsOn agent serves
// next to Magic Cache.
package bazel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

const (
	// registerPath is where the agent gives the job's runtime token a cache
	// path. Agents without the Bazel cache answer 404.
	registerPath = "/bazel"

	blockStart = "# BEGIN runs-on/action bazel_remote_cache"
	blockEnd   = "# END runs-on/action bazel_remote_cache"
)

var errUnsupported = errors.New("the RunsOn agent does not serve a Bazel remote cache")

// systemRC is Bazel's system rc. Bazel reads it before the workspace and user
// rc files, so a project's own --remote_cache wins. No flag or variable moves
// it.
var systemRC = func() string {
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return filepath.Join(programData, "bazel.bazelrc")
	}
	return "/etc/bazel.bazelrc"
}

// Configure points Bazel at the RunsOn agent's Bazel remote cache, which
// stores entries in Magic Cache's bucket with GitHub's cache scoping. Bazel's
// HTTP cache client sends no token, so the job's runtime token is registered
// with the agent, which answers with a cache path that stands for it. The
// path goes into Bazel's system rc as --remote_cache. When anything is
// missing, Configure warns and Bazel builds without the remote cache.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	if mode != "true" {
		action.Warningf("bazel_remote_cache is %q, want \"true\"; Bazel builds without the remote cache.", mode)
		return
	}
	if !magicCache {
		action.Warningf("bazel_remote_cache needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); Bazel builds without the remote cache.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("bazel_remote_cache needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; Bazel builds without the remote cache.")
		return
	}
	cacheURL, err := register(magiccache.URL(), runtimeToken)
	if errors.Is(err, errUnsupported) {
		action.Warningf("bazel_remote_cache needs RunsOn v3.5.1 or later, whose agent serves a Bazel remote cache; Bazel builds without the remote cache.")
		return
	}
	if err != nil {
		action.Warningf("bazel_remote_cache could not register the job with the RunsOn agent (%v); Bazel builds without the remote cache.", err)
		return
	}
	// The path gives the job's cache scopes to whatever can reach the agent,
	// until the token expires.
	action.AddMask(cacheURL)
	path := systemRC()
	if err := writeSystemRC(path, cacheURL); err != nil {
		action.Warningf("bazel_remote_cache could not write %s (%v); Bazel builds without the remote cache.", path, err)
		return
	}
	action.Infof("Wrote --remote_cache to %s: Bazel uses Magic Cache as its remote cache in the next steps, unless the project sets its own --remote_cache.", path)
}

// register sends the job's runtime token to the agent behind the Magic Cache
// URL and returns the --remote_cache URL the agent gave it.
func register(magicCacheURL, token string) (string, error) {
	base, err := url.Parse(magicCacheURL)
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("invalid Magic Cache URL %q", magicCacheURL)
	}
	request, err := http.NewRequest(http.MethodPost, base.ResolveReference(&url.URL{Path: registerPath}).String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return "", errUnsupported
	default:
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return "", fmt.Errorf("the agent answered %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var registration struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&registration); err != nil {
		return "", fmt.Errorf("read the agent's answer: %w", err)
	}
	if !strings.HasPrefix(registration.Path, registerPath+"/") {
		return "", fmt.Errorf("the agent answered an unexpected path %q", registration.Path)
	}
	return base.ResolveReference(&url.URL{Path: registration.Path}).String(), nil
}

// writeSystemRC sets --remote_cache in Bazel's system rc. It replaces its own
// block, so running the action again does not repeat it, and keeps whatever
// else the file holds, such as an image's settings.
func writeSystemRC(path, cacheURL string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var kept []string
	inBlock := false
	for line := range strings.SplitSeq(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n") {
		switch {
		case line == blockStart:
			inBlock = true
		case line == blockEnd:
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	kept = append(kept, blockStart, "common --remote_cache="+cacheURL, blockEnd)
	content := []byte(strings.Join(kept, "\n") + "\n")

	err = os.WriteFile(path, content, 0o644)
	if err == nil || runtime.GOOS == "windows" || !errors.Is(err, os.ErrPermission) {
		return err
	}
	// /etc belongs to root; RunsOn runners give the runner user sudo.
	cmd := exec.Command("sudo", "-n", "tee", path)
	cmd.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo tee: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
