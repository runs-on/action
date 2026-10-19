// Package bazel points Bazel at the remote cache that the RunsOn agent serves
// next to Magic Cache.
package bazel

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

const (
	blockStart = "# BEGIN runs-on/action bazel_remote_cache"
	blockEnd   = "# END runs-on/action bazel_remote_cache"
)

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
// with the agent, which answers with a path that stands for it. The cache URL
// goes into Bazel's system rc as --remote_cache. When anything is missing,
// Configure warns and Bazel builds without the remote cache.
func Configure(action *githubactions.Action, mode string, magicCache bool, runtimeToken string) {
	const fallback = "Bazel builds without the remote cache"
	if mode != "true" {
		action.Warningf("bazel_remote_cache is %q, want \"true\"; %s.", mode, fallback)
		return
	}
	tools, ok := magiccache.RegisterFor(action, "bazel_remote_cache", fallback, magicCache, runtimeToken)
	if !ok {
		return
	}
	path := systemRC()
	if err := writeSystemRC(path, tools.URL+"/bazel"); err != nil {
		action.Warningf("bazel_remote_cache could not write %s (%v); %s.", path, err, fallback)
		return
	}
	action.Infof("Wrote --remote_cache to %s: Bazel uses Magic Cache as its remote cache in the next steps, unless the project sets its own --remote_cache.", path)
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
