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
	// Root indicates the cached paths are owned by root (e.g. apt).
	Root bool
	// Post runs after the paths have been bind-mounted (e.g. apt config fixups).
	Post func(action *githubactions.Action) error
}

var cacheModes = map[string]CacheMode{
	"go":         {Name: "go", Paths: []string{"~/.cache/go-build", "~/go/pkg/mod"}},
	"node":       {Name: "node", Paths: []string{"~/.npm"}},
	"yarn":       {Name: "yarn", Paths: []string{"~/.cache/yarn"}},
	"pnpm":       {Name: "pnpm", Paths: []string{"~/.pnpm-store"}},
	"ruby":       {Name: "ruby", Paths: []string{"~/.bundle", "vendor/bundle"}},
	"rust":       {Name: "rust", Paths: []string{"~/.cargo/registry", "~/.cargo/git"}},
	"python":     {Name: "python", Paths: []string{"~/.cache/pip"}},
	"uv":         {Name: "uv", Paths: []string{"~/.cache/uv"}},
	"poetry":     {Name: "poetry", Paths: []string{"~/.cache/pypoetry"}},
	"apt":        {Name: "apt", Paths: []string{"/var/cache/apt/archives"}, Root: true, Post: configureApt},
	"gradle":     {Name: "gradle", Paths: []string{"~/.gradle/caches", "~/.gradle/wrapper"}},
	"maven":      {Name: "maven", Paths: []string{"~/.m2/repository"}},
	"playwright": {Name: "playwright", Paths: []string{"~/.cache/ms-playwright"}},
}

var modeAliases = map[string]string{
	"golang":  "go",
	"npm":     "node",
	"pip":     "python",
	"bundler": "ruby",
	"cargo":   "rust",
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
