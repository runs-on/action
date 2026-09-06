package config

import (
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

	t.Setenv("RUNS_ON_RUNNER_NAME", "")
	if cfg.HasSccache() {
		t.Fatal("sccache was enabled outside RunsOn")
	}
}

func TestSccachePrefixInput(t *testing.T) {
	t.Setenv("INPUT_SCCACHE", "s3")
	t.Setenv("INPUT_SCCACHE_PREFIX", "cache/sccache")

	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.SccachePrefix, "cache/sccache"; got != want {
		t.Fatalf("SccachePrefix = %q, want %q", got, want)
	}

	t.Setenv("INPUT_SCCACHE_PREFIX", "")
	cfg, err = NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SccachePrefix != "" {
		t.Fatalf("SccachePrefix = %q, want the default prefix to be resolved later", cfg.SccachePrefix)
	}
}

func TestStickyCacheInputs(t *testing.T) {
	t.Setenv("INPUT_STICKY_CACHE", " go \n\n buildkit \n custom,path=vendor/cache ")
	t.Setenv("INPUT_STICKY_WAIT_TIMEOUT", "90s")

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
}

func TestStickyWaitTimeoutDefaultsToAgentWaitWindow(t *testing.T) {
	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.StickyWaitTimeout, 15*time.Minute; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
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
