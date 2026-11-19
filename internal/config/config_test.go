package config

import (
	"io"
	"testing"
	"time"

	"github.com/sethvargo/go-githubactions"
)

func TestHasSccacheOnRunsOn(t *testing.T) {
	t.Setenv("RUNS_ON_RUNNER_NAME", "windows-runner")
	cfg := Config{Sccache: "s3"}

	if !cfg.HasSccache() {
		t.Fatal("sccache was disabled on a RunsOn runner")
	}

	if (&Config{Sccache: "false"}).HasSccache() {
		t.Fatal("sccache: false enabled sccache")
	}

	t.Setenv("RUNS_ON_RUNNER_NAME", "")
	if cfg.HasSccache() {
		t.Fatal("sccache was enabled outside RunsOn")
	}
}

func TestStickyCacheInputs(t *testing.T) {
	t.Setenv("INPUT_STICKY_CACHE", " go \n\n buildkit \n custom,path=vendor/cache ")
	t.Setenv("INPUT_STICKY_WAIT_TIMEOUT", "90s")
	t.Setenv("INPUT_STICKY_SAVE", "false")

	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StickyCache) != 3 {
		t.Fatalf("StickyCache = %#v, want three records", cfg.StickyCache)
	}
	if got, want := cfg.StickyCache[1], "buildkit"; got != want {
		t.Fatalf("StickyCache[1] = %q, want %q", got, want)
	}
	if got, want := cfg.StickyWaitTimeout, 90*time.Second; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
	}
	if cfg.StickySave {
		t.Fatal("StickySave = true, want false")
	}
}

func TestStickyWaitTimeoutDefaultsToAgentWaitWindow(t *testing.T) {
	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.StickyWaitTimeout, 15*time.Minute; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
	}
	if !cfg.StickySave {
		t.Fatal("StickySave defaults to false, want true")
	}
}

func TestStickyWaitTimeoutRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"typo", "0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("INPUT_STICKY_WAIT_TIMEOUT", value)
			if _, err := NewConfigFromInputs(githubactions.New()); err == nil {
				t.Fatalf("sticky_wait_timeout %q was accepted", value)
			}
		})
	}
}

func TestStickyCacheRequestIsHandledOutsideRunsOn(t *testing.T) {
	t.Setenv("RUNS_ON_RUNNER_NAME", "")
	cfg := Config{StickyCache: []string{"go"}}
	if !cfg.HasStickyDiskCache() {
		t.Fatal("sticky cache request was silently ignored outside RunsOn")
	}
}

func TestRestoreOnlyAloneConfiguresStickyDisk(t *testing.T) {
	// A later invocation may set only sticky_save: false for caches an
	// earlier invocation mounted.
	cfg := Config{StickySave: false}
	if !cfg.ConfiguresStickyDisk() {
		t.Fatal("sticky_save: false without sticky_cache was ignored")
	}
}

func TestUndeclaredLegacyInputsRemainIgnored(t *testing.T) {
	t.Setenv("INPUT_CACHE", "go,buildkit")
	t.Setenv("INPUT_PATH", "vendor/cache")
	t.Setenv("INPUT_WAIT_TIMEOUT", "1s")
	t.Setenv("INPUT_FAIL_ON_MISSING", "true")

	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StickyCache) != 0 {
		t.Fatalf("StickyCache = %#v, want undeclared legacy inputs ignored", cfg.StickyCache)
	}
	if got, want := cfg.StickyWaitTimeout, 15*time.Minute; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
	}
}

func TestHasMagicCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resultsURL string
		cacheURL   string
		customURL  string
		want       bool
	}{
		{name: "no Magic Cache"},
		{name: "GHES v1 cache URL only", cacheURL: "https://ghes.example/_services/artifactcache/", want: true},
		{name: "results URL only", resultsURL: "https://results.example/", want: true},
		{name: "both original URLs", cacheURL: "https://ghes.example/_services/artifactcache/", resultsURL: "https://results.example/", want: true},
		{name: "custom runner", customURL: "http://localhost:6871/", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZCTIONS_RESULTS_URL", tc.resultsURL)
			t.Setenv("ZCTIONS_CACHE_URL", tc.cacheURL)
			t.Setenv("RUNS_ON_MAGIC_CACHE_URL", tc.customURL)
			// GitHub's standard URLs also exist without Magic Cache, so they
			// alone must not cause its cache to be treated as the agent's.
			t.Setenv("ACTIONS_RESULTS_URL", "https://results.example/")
			t.Setenv("ACTIONS_CACHE_URL", "https://ghes.example/_services/artifactcache/")
			cfg, err := NewConfigFromInputs(githubactions.New(githubactions.WithWriter(io.Discard)))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.HasMagicCache(); got != tc.want {
				t.Fatalf("HasMagicCache() = %t, want %t", got, tc.want)
			}
		})
	}
}
