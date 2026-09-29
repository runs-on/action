package stickydisk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sethvargo/go-githubactions"
)

// CacheMode describes the directories a given package manager expects to be
// persisted. Paths starting with "~/" are relative to the runner user's home
// directory, relative paths are resolved against GITHUB_WORKSPACE, absolute
// paths are used as-is.
type CacheMode struct {
	Name string
	// Paths to persist on the sticky disk.
	Paths []string
	// PathEnv names an environment variable whose value is the single path to
	// persist. It is used for runner-defined locations such as the tool cache.
	PathEnv string
	// IgnoreTargetContents prevents the current runner image or checkout from
	// seeding the sticky source before it is mounted over the target.
	IgnoreTargetContents bool
	// WindowsPaths override Paths on Windows runners, where several package
	// managers cache under ~/AppData instead of ~/.cache. Empty means Paths
	// apply on Windows too.
	WindowsPaths []string
	// Root indicates the cached paths are owned by root (e.g. apt).
	// Root modes are Linux-only.
	Root bool
	// Post runs after the paths have been bind-mounted (e.g. apt config fixups).
	Post func(action *githubactions.Action) error
	// Setup, when set, replaces the Paths bind-mount mechanism entirely: the
	// mode owns its whole setup (e.g. buildkit prepares a Docker volume).
	Setup func(action *githubactions.Action, mountRoot string) (hit bool, err error)
	// SetupFailureFatal is reserved for setup contracts that must not silently
	// degrade to an uncached implementation.
	SetupFailureFatal bool
	// PostJob runs during the action's post step, before the sticky disk is
	// unmounted and snapshotted (e.g. stop the Buildx builder consistently).
	PostJob func(action *githubactions.Action) error
	// PostJobFailureFatal protects snapshot consistency when cleanup cannot be
	// completed safely.
	PostJobFailureFatal bool
}

var cacheModes = map[string]CacheMode{
	"go":         {Name: "go", Paths: []string{"~/.cache/go-build", "~/go/pkg/mod"}, WindowsPaths: []string{"~/AppData/Local/go-build", "~/go/pkg/mod"}},
	"node":       {Name: "node", Paths: []string{"~/.npm"}, WindowsPaths: []string{"~/AppData/Local/npm-cache"}},
	"yarn":       {Name: "yarn", Paths: []string{"~/.cache/yarn"}, WindowsPaths: []string{"~/AppData/Local/Yarn/Cache"}},
	"pnpm":       {Name: "pnpm", Paths: []string{"~/.local/share/pnpm/store"}, WindowsPaths: []string{"~/AppData/Local/pnpm/store"}},
	"ruby":       {Name: "ruby", Paths: []string{"~/.bundle/cache", "vendor/bundle"}},
	"rust":       {Name: "rust", Paths: []string{"~/.cargo/registry", "~/.cargo/git"}},
	"python":     {Name: "python", Paths: []string{"~/.cache/pip"}, WindowsPaths: []string{"~/AppData/Local/pip/cache"}},
	"uv":         {Name: "uv", Paths: []string{"~/.cache/uv"}, WindowsPaths: []string{"~/AppData/Local/uv/cache"}},
	"poetry":     {Name: "poetry", Paths: []string{"~/.cache/pypoetry"}, WindowsPaths: []string{"~/AppData/Local/pypoetry/Cache"}},
	"apt":        {Name: "apt", Paths: []string{"/var/cache/apt/archives"}, Root: true, Post: configureApt, PostJob: restoreApt},
	"buildkit":   {Name: "buildkit", Setup: setupBuildkit, SetupFailureFatal: true, PostJob: cleanupBuildkit, PostJobFailureFatal: true},
	"git":        {Name: "git", Setup: setupGit, PostJob: stopGit, PostJobFailureFatal: true},
	"git-full":   {Name: "git-full", Setup: setupGitFull, PostJob: stopGit, PostJobFailureFatal: true},
	"gradle":     {Name: "gradle", Paths: []string{"~/.gradle/caches", "~/.gradle/wrapper"}},
	"maven":      {Name: "maven", Paths: []string{"~/.m2/repository"}},
	"playwright": {Name: "playwright", Paths: []string{"~/.cache/ms-playwright"}, WindowsPaths: []string{"~/AppData/Local/ms-playwright"}},
	"tool-cache": {Name: "tool-cache", PathEnv: "RUNNER_TOOL_CACHE", IgnoreTargetContents: true},
}

var modeAliases = map[string]string{
	"golang":   "go",
	"npm":      "node",
	"pip":      "python",
	"bundler":  "ruby",
	"cargo":    "rust",
	"buildx":   "buildkit",
	"checkout": "git",
}

// pathsFor returns the cache paths for the given OS.
func (m CacheMode) pathsFor(goos string) ([]string, error) {
	if m.PathEnv != "" {
		path := strings.TrimSpace(os.Getenv(m.PathEnv))
		if path == "" {
			return nil, fmt.Errorf("cache mode %q requires the runner to set %s", m.Name, m.PathEnv)
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("cache mode %q requires %s to be an absolute path, got %q", m.Name, m.PathEnv, path)
		}
		return []string{filepath.Clean(path)}, nil
	}
	if goos == "windows" && len(m.WindowsPaths) > 0 {
		return m.WindowsPaths, nil
	}
	if m.Name == "pnpm" {
		if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
			return []string{filepath.Join(dataHome, "pnpm", "store")}, nil
		}
	}
	return m.Paths, nil
}

// supportedOn reports whether the mode works on the given OS: root-owned
// paths (apt) and daemon setups (buildkit) are Linux-only.
func (m CacheMode) supportedOn(goos string) bool {
	if goos != "windows" {
		return true
	}
	return !m.Root && m.Setup == nil
}

// ValidModes returns the sorted list of supported cache mode names.
func ValidModes() []string {
	names := make([]string, 0, len(cacheModes))
	for name := range cacheModes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
