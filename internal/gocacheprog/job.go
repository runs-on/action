package gocacheprog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runs-on/action/internal/magiccache"
)

// A job keeps these files next to its local entries: the journal of the
// action IDs its go commands used, and the list its prefetch downloaded,
// written once the prefetch is over. Both are concatenated 32-byte action
// IDs. An empty third file marks a list the prefetch could not bring whole.
const (
	usedFile       = "used"
	prefetchedFile = "prefetched"
	staleFile      = "stale"
)

// ListKey names the list of entries a job used, which the job's next run
// prefetches. The go command does not tell the helper what it builds, but a
// job runs the same go commands from one run to the next, and GitHub names
// it: the runtime token's orch_id claim is "<run>.<job>.<matrix leg>".
// Without that claim the job's key in its workflow stands in, and the legs of
// a matrix share a list.
func ListKey(token string) string {
	job := jobFromToken(token)
	if job == "" {
		job = os.Getenv("GITHUB_JOB")
	}
	workflow, _, _ := strings.Cut(os.Getenv("GITHUB_WORKFLOW_REF"), "@")
	identity := strings.Join([]string{workflow, job, os.Getenv("RUNNER_OS"), os.Getenv("RUNNER_ARCH")}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return namespace + "-list-" + hex.EncodeToString(sum[:8])
}

func jobFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		OrchID string `json:"orch_id"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	_, job, _ := strings.Cut(claims.OrchID, ".")
	return job
}

func listVersion(opts Options) string {
	return opts.Namespace + "-lists"
}

// listUploadTimeout bounds the upload of a job's list, which the action's
// post step waits for: a cache that stops answering must not hold up the end
// of the job for the HTTP client's five-minute limit.
var listUploadTimeout = 30 * time.Second

// Prefetch downloads the entries the job's previous run used, so its go
// commands find them on local disk instead of asking for them one at a time.
// The action starts it in the background when the job starts.
func Prefetch(opts Options, listKey string) error {
	c, err := New(opts)
	if err != nil {
		return err
	}
	defer c.cancel()
	if c.remote == nil {
		return nil
	}
	var list []byte
	body, _, ok, err := c.remote.Open(c.ctx, listVersion(opts), listKey, listKey+"-")
	if err == nil && ok {
		list, err = io.ReadAll(body)
		_ = body.Close()
	}
	if err != nil {
		return err
	}
	list = list[:len(list)-len(list)%actionIDSize]
	sem := make(chan struct{}, c.opts.Concurrency)
	var wg sync.WaitGroup
	var missed atomic.Bool
	for i := 0; i < len(list); i += actionIDSize {
		actionID := list[i : i+actionIDSize]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if !c.fetch(actionID) {
				missed.Store(true)
			}
		})
	}
	wg.Wait()
	// A list with entries that are gone, or that this job may not use (the
	// test records of a run that shared them), costs every run a lookup per
	// such entry: the post step replaces it.
	if missed.Load() {
		if err := os.WriteFile(filepath.Join(opts.Dir, staleFile), nil, 0o644); err != nil {
			return err
		}
	}
	// Written even when empty: the post step compares what the job used with
	// it.
	_, err = c.disk.writeAtomically(filepath.Join(opts.Dir, prefetchedFile), bytes.NewReader(list), nil)
	return err
}

// SaveList saves the entries the job's go commands used, in the order they
// first used them, for the job's next run to prefetch. It returns how many it
// saved. Lists are saved under a new key each time, and a lookup by prefix
// returns the newest. A job that only used entries it prefetched saves
// nothing, so a job that keeps running unchanged does not add a list per run;
// the entries it no longer uses stay in its list until a run uses a new one.
// A list the prefetch could not bring whole is replaced.
func SaveList(opts Options, listKey string) (int, error) {
	if opts.ResultsURL == "" || opts.Token == "" {
		return 0, nil
	}
	journal, _ := os.ReadFile(filepath.Join(opts.Dir, usedFile))
	prefetched, _ := os.ReadFile(filepath.Join(opts.Dir, prefetchedFile))
	known := map[[actionIDSize]byte]bool{}
	for i := 0; i+actionIDSize <= len(prefetched); i += actionIDSize {
		known[[actionIDSize]byte(prefetched[i:i+actionIDSize])] = true
	}
	var list []byte
	seen := map[[actionIDSize]byte]bool{}
	changed := false
	for i := 0; i+actionIDSize <= len(journal); i += actionIDSize {
		id := [actionIDSize]byte(journal[i : i+actionIDSize])
		if seen[id] {
			continue
		}
		seen[id] = true
		list = append(list, id[:]...)
		changed = changed || !known[id]
	}
	stale := filepath.Join(opts.Dir, staleFile)
	if _, err := os.Stat(stale); err == nil {
		changed = true
	}
	if !changed || len(list) == 0 {
		return 0, nil
	}
	key := listKey + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	client := magiccache.NewCacheClient(opts.ResultsURL, opts.Token)
	ctx, cancel := context.WithTimeout(context.Background(), listUploadTimeout)
	defer cancel()
	if err := client.Upload(ctx, listVersion(opts), key, bytes.NewReader(list), int64(len(list))); err != nil {
		return 0, err
	}
	// A second runs-on/action step in the job has nothing more to save.
	_ = os.WriteFile(filepath.Join(opts.Dir, prefetchedFile), list, 0o644)
	_ = os.Remove(stale)
	return len(list) / actionIDSize, nil
}
