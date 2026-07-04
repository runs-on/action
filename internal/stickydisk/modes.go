package stickydisk

import (
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
	// mode owns its whole setup (e.g. buildkit runs a daemon off the disk).
	Setup func(action *githubactions.Action, mountRoot string) (hit bool, err error)
	// PostJob runs during the action's post step, before the sticky disk is
	// unmounted and snapshotted (e.g. stop buildkitd for a consistent state).
	PostJob func(action *githubactions.Action) error
}

var cacheModes = map[string]CacheMode{
	"go":         {Name: "go", Paths: []string{"~/.cache/go-build", "~/go/pkg/mod"}, WindowsPaths: []string{"~/AppData/Local/go-build", "~/go/pkg/mod"}},
	"node":       {Name: "node", Paths: []string{"~/.npm"}, WindowsPaths: []string{"~/AppData/Local/npm-cache"}},
	"yarn":       {Name: "yarn", Paths: []string{"~/.cache/yarn"}, WindowsPaths: []string{"~/AppData/Local/Yarn/Cache"}},
	"pnpm":       {Name: "pnpm", Paths: []string{"~/.pnpm-store"}, WindowsPaths: []string{"~/AppData/Local/pnpm/store"}},
	"ruby":       {Name: "ruby", Paths: []string{"~/.bundle", "vendor/bundle"}},
	"rust":       {Name: "rust", Paths: []string{"~/.cargo/registry", "~/.cargo/git"}},
	"python":     {Name: "python", Paths: []string{"~/.cache/pip"}, WindowsPaths: []string{"~/AppData/Local/pip/cache"}},
	"uv":         {Name: "uv", Paths: []string{"~/.cache/uv"}, WindowsPaths: []string{"~/AppData/Local/uv/cache"}},
	"poetry":     {Name: "poetry", Paths: []string{"~/.cache/pypoetry"}, WindowsPaths: []string{"~/AppData/Local/pypoetry/Cache"}},
	"apt":        {Name: "apt", Paths: []string{"/var/cache/apt/archives"}, Root: true, Post: configureApt},
	"buildkit":   {Name: "buildkit", Setup: setupBuildkit, PostJob: stopBuildkit},
	"gradle":     {Name: "gradle", Paths: []string{"~/.gradle/caches", "~/.gradle/wrapper"}},
	"maven":      {Name: "maven", Paths: []string{"~/.m2/repository"}},
	"playwright": {Name: "playwright", Paths: []string{"~/.cache/ms-playwright"}, WindowsPaths: []string{"~/AppData/Local/ms-playwright"}},
}

var modeAliases = map[string]string{
	"golang":  "go",
	"npm":     "node",
	"pip":     "python",
	"bundler": "ruby",
	"cargo":   "rust",
	"buildx":  "buildkit",
}

// pathsFor returns the cache paths for the given OS.
func (m CacheMode) pathsFor(goos string) []string {
	if goos == "windows" && len(m.WindowsPaths) > 0 {
		return m.WindowsPaths
	}
	return m.Paths
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

// ResolveModes maps user-provided mode names (already split into a list) to
// their canonical CacheMode definitions, deduplicating and preserving order.
// Unknown modes are returned separately so callers can warn about them.
func ResolveModes(inputs []string) (modes []CacheMode, unknown []string) {
	seen := map[string]bool{}
	for _, input := range inputs {
		name := strings.ToLower(strings.TrimSpace(input))
		if name == "" {
			continue
		}
		if canonical, ok := modeAliases[name]; ok {
			name = canonical
		}
		mode, ok := cacheModes[name]
		if !ok {
			unknown = append(unknown, input)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		modes = append(modes, mode)
	}
	return modes, unknown
}
