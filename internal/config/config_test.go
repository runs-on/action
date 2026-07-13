package config

import (
	"testing"
	"time"

	"github.com/sethvargo/go-githubactions"
)

func TestStickyCacheInputs(t *testing.T) {
	t.Setenv("INPUT_STICKY_CACHE", " go \n\n buildkit,fail-on-missing=true \n custom,path=vendor/cache ")
	t.Setenv("INPUT_STICKY_WAIT_TIMEOUT", "90s")

	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StickyCache) != 3 {
		t.Fatalf("StickyCache = %#v, want three records", cfg.StickyCache)
	}
	if got, want := cfg.StickyCache[1], "buildkit,fail-on-missing=true"; got != want {
		t.Fatalf("StickyCache[1] = %q, want %q", got, want)
	}
	if got, want := cfg.StickyWaitTimeout, 90*time.Second; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
	}
}

func TestStickyWaitTimeoutDefaultsToFiveMinutes(t *testing.T) {
	cfg, err := NewConfigFromInputs(githubactions.New())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.StickyWaitTimeout, 5*time.Minute; got != want {
		t.Fatalf("StickyWaitTimeout = %s, want %s", got, want)
	}
}
