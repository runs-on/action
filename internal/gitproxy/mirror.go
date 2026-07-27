// Package gitproxy implements a local git smart-HTTP proxy backed by bare
// repository mirrors stored on the sticky disk. Fetch URLs are rewritten to
// it via git's url.<base>.insteadOf mechanism, so an unmodified
// actions/checkout (or any git fetch of a github.com repo) is served from
// local storage, with only new objects crossing the network once per job.
//
// Adapted from github.com/crohr/smart-git-proxy (internal/mirror,
// internal/gitproxy). Protocol serving is delegated to `git http-backend`
// (see cgi.go) instead of a hand-rolled smart-HTTP implementation, and the
// shared-proxy pack cache was dropped: a per-job proxy serves each pack once.
package gitproxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Status indicates what EnsureRepo had to do before a repo could be served.
type Status string

const (
	StatusHit   Status = "mirror-hit"   // served from an existing fresh mirror
	StatusClone Status = "mirror-clone" // had to clone a new mirror
	StatusSync  Status = "mirror-sync"  // had to sync a stale mirror
)

// Mirror manages bare git repository mirrors under a root directory.
type Mirror struct {
	root       string
	staleAfter time.Duration
	log        *slog.Logger
	ctx        context.Context
	cancel     context.CancelFunc

	group      singleflight.Group
	maintGroup singleflight.Group
	maintMu    sync.Mutex
	maintWG    sync.WaitGroup
	closed     bool
	lastSync   sync.Map // map[repoKey]time.Time
	syncFailed sync.Map // map[repoKey]bool; keeps protocol-v2 follow-ups upstream
	authorized sync.Map // map[repoKey][32]byte; hashes successful private-repo auth
}

// NewMirror creates a mirror manager rooted at root. lastSync tracking is
// in-memory only: a proxy process is fresh per job, so the first request for
// each repo always syncs from upstream — guaranteeing that the just-pushed
// SHA a workflow runs against is present in the mirror.
func NewMirror(root string, staleAfter time.Duration, log *slog.Logger) (*Mirror, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create mirror root: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Mirror{root: root, staleAfter: staleAfter, log: log, ctx: ctx, cancel: cancel}, nil
}

// RepoPath returns the filesystem path for a repo mirror.
func (m *Mirror) RepoPath(host, owner, repo string) string {
	return filepath.Join(m.root, host, owner, repo+".git")
}

// EnsureRepo ensures the mirror exists and is fresh enough to serve.
// authHeader is the Authorization header value from the client request (can
// be empty). Returns the path to the bare repo and what had to be done.
func (m *Mirror) EnsureRepo(ctx context.Context, host, owner, repo, upstreamURL, authHeader string) (string, Status, error) {
	start := time.Now()
	repoPath := m.RepoPath(host, owner, repo)
	key := fmt.Sprintf("%s/%s/%s", host, owner, repo)

	// Clone goes through singleflight so a concurrent request never sees a
	// half-created directory: it waits for the in-flight clone instead.
	result, err, _ := m.group.Do("clone:"+key, func() (interface{}, error) {
		_, statErr := os.Stat(repoPath)
		if statErr == nil && !m.IsUsable(ctx, repoPath) {
			m.log.Warn("removing unusable persisted mirror", "repo", key, "path", repoPath)
			if err := os.RemoveAll(repoPath); err != nil {
				return StatusClone, fmt.Errorf("remove unusable mirror: %w", err)
			}
			statErr = os.ErrNotExist
		}
		if statErr == nil {
			if _, seen := m.lastSync.Load(key); !seen {
				if err := configureMirrorUploadPack(ctx, repoPath); err != nil {
					// Protocol v2 follows the failed info/refs request with
					// want-less requests. Keep those upstream just as we do
					// after a fetch failure, or stale local refs can leak into
					// the remainder of the negotiation.
					m.syncFailed.Store(key, true)
					return StatusHit, err
				}
			}
		}
		if os.IsNotExist(statErr) {
			if err := m.cloneRepo(ctx, repoPath, upstreamURL, authHeader); err != nil {
				// cloneRepo can fail after creating a usable bare repository
				// (for example during upload-pack configuration). Keep every
				// protocol-v2 follow-up upstream until a later setup succeeds.
				if m.IsUsable(ctx, repoPath) {
					m.syncFailed.Store(key, true)
				}
				return StatusClone, err
			}
			m.markAuthorized(key, repoPath, authHeader)
			m.lastSync.Store(key, time.Now())
			return StatusClone, nil
		}
		if statErr != nil {
			return StatusHit, fmt.Errorf("inspect mirror: %w", statErr)
		}
		return StatusHit, nil
	})
	if err != nil {
		return "", "", err
	}
	if result.(Status) == StatusClone {
		m.syncFailed.Delete(key)
		if err := m.authorizeRepo(ctx, key, repoPath, upstreamURL, authHeader); err != nil {
			return "", "", fmt.Errorf("authentication required: %w", err)
		}
		m.log.Info("mirror cloned", "repo", key, "duration_ms", time.Since(start).Milliseconds())
		return repoPath, StatusClone, nil
	}

	if m.isStale(key) {
		_, err, _ := m.group.Do("sync:"+key, func() (interface{}, error) {
			// A repository can become private after its public mirror was
			// created. Persist that boundary before fetching potentially
			// private objects into the previously public mirror.
			if authHeader != "" && !m.requiresAuth(repoPath) {
				if err := markPrivateMirror(repoPath); err != nil {
					return nil, err
				}
			}
			if err := m.syncRepo(ctx, repoPath, upstreamURL, authHeader); err != nil {
				return nil, err
			}
			m.markAuthorized(key, repoPath, authHeader)
			return nil, nil
		})
		if err != nil {
			// Protocol v2 sends want-less follow-ups after info/refs. Once a
			// sync fails, keep every follow-up upstream for both public and
			// private mirrors so stale local refs cannot leak into the fetch.
			m.syncFailed.Store(key, true)
			if m.requiresAuth(repoPath) {
				return "", "", fmt.Errorf("authentication required: %w", err)
			}
			// Serving a stale advertisement can make newly created refs fail
			// before the client sends wants, so let the HTTP handler forward
			// the complete request upstream instead.
			return "", "", fmt.Errorf("sync public mirror: %w", err)
		}
		m.syncFailed.Delete(key)
		m.lastSync.Store(key, time.Now())
		if err := m.authorizeRepo(ctx, key, repoPath, upstreamURL, authHeader); err != nil {
			return "", "", fmt.Errorf("authentication required: %w", err)
		}
		m.scheduleOptimize(repoPath, false)
		m.log.Info("mirror synced", "repo", key, "duration_ms", time.Since(start).Milliseconds())
		return repoPath, StatusSync, nil
	}

	// Fresh mirror: still validate auth for private repos so an
	// unauthenticated client cannot read a previously-mirrored private repo.
	if err := m.authorizeRepo(ctx, key, repoPath, upstreamURL, authHeader); err != nil {
		return "", "", fmt.Errorf("authentication required: %w", err)
	}
	return repoPath, StatusHit, nil
}

// SyncFailed reports whether this proxy must keep a repository's protocol-v2
// follow-up requests upstream until a later info/refs sync succeeds.
func (m *Mirror) SyncFailed(host, owner, repo string) bool {
	_, failed := m.syncFailed.Load(fmt.Sprintf("%s/%s/%s", host, owner, repo))
	return failed
}

// isStale returns true if the repo needs syncing.
func (m *Mirror) isStale(key string) bool {
	lastSync, ok := m.lastSync.Load(key)
	if !ok {
		return true
	}
	return time.Since(lastSync.(time.Time)) > m.staleAfter
}

// requiresAuth checks if a repo was cloned with authentication.
func (m *Mirror) requiresAuth(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".requires-auth"))
	return err == nil
}

// authorizeRepo prevents a direct protocol-v2 POST from reading a restored
// private mirror without first proving upstream access. Successful headers
// are hashed in memory so the normal info/refs + upload-pack sequence does not
// add another upstream round trip or retain credentials in proxy state.
func (m *Mirror) authorizeRepo(ctx context.Context, key, repoPath, upstreamURL, authHeader string) error {
	if !m.requiresAuth(repoPath) {
		return nil
	}
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}
	digest := sha256.Sum256([]byte(authHeader))
	if authorized, ok := m.authorized.Load(key); ok && authorized == digest {
		return nil
	}
	if err := m.validateAuth(ctx, upstreamURL, authHeader); err != nil {
		return err
	}
	m.authorized.Store(key, digest)
	return nil
}

func (m *Mirror) markAuthorized(key, repoPath, authHeader string) {
	if m.requiresAuth(repoPath) && authHeader != "" {
		m.authorized.Store(key, sha256.Sum256([]byte(authHeader)))
	}
}

// validateAuth validates the auth token can access the upstream repo.
func (m *Mirror) validateAuth(ctx context.Context, upstreamURL, authHeader string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "-q", upstreamURL, "HEAD")
	cmd.Env = gitEnv(authHeader)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git ls-remote failed: %w\noutput: %s", err, output)
	}
	return nil
}

// cloneRepo creates a new bare mirror.
func (m *Mirror) cloneRepo(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	m.log.Info("cloning mirror", "path", repoPath, "hasAuth", authHeader != "")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Disable GC and delta recompression during clone to reduce CPU/memory
	// pressure: the received pack is stored as-is, optimizeRepo repacks later.
	args := []string{
		"-c", "gc.auto=0",
		"-c", "core.compression=0",
		"-c", "pack.window=0",
		"-c", "pack.depth=0",
		"-c", "pack.deltaCacheSize=1",
		"-c", "pack.threads=1",
		"clone", "--mirror", "--", upstreamURL, repoPath,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv(authHeader)
	if output, err := cmd.CombinedOutput(); err != nil {
		// git may leave a partial directory behind; remove it so the next
		// attempt does not mistake it for a valid mirror.
		os.RemoveAll(repoPath)
		return fmt.Errorf("git clone failed: %w\noutput: %s", err, output)
	}

	// Mark authenticated mirrors before any fallible post-clone setup. If
	// setup fails, the usable bare repository must still fail closed.
	if authHeader != "" {
		if err := markPrivateMirror(repoPath); err != nil {
			return err
		}
	}

	// actions/checkout fetches exact commit SHAs and sparse checkouts fetch
	// with --filter=blob:none; upload-pack rejects both unless the mirror
	// repo config allows them.
	if err := configureMirrorUploadPack(ctx, repoPath); err != nil {
		return err
	}

	m.scheduleOptimize(repoPath, true)
	return nil
}

// markPrivateMirror fails closed: without this persisted marker, a future
// proxy process would treat cached private objects as public. Remove the
// mirror if the marker cannot be written so no unsafe snapshot can survive.
func markPrivateMirror(repoPath string) error {
	marker := filepath.Join(repoPath, ".requires-auth")
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		markerErr := fmt.Errorf("mark private mirror %s: %w", repoPath, err)
		if cleanupErr := os.RemoveAll(repoPath); cleanupErr != nil {
			return errors.Join(markerErr, fmt.Errorf("remove unmarked private mirror: %w", cleanupErr))
		}
		return markerErr
	}
	return nil
}

func configureMirrorUploadPack(ctx context.Context, repoPath string) error {
	for _, kv := range [][2]string{
		{"uploadpack.allowAnySHA1InWant", "true"},
		{"uploadpack.allowFilter", "true"},
	} {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "config", kv[0], kv[1])
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s failed: %w\noutput: %s", kv[0], err, output)
		}
	}
	return nil
}

// syncRepo fetches updates from upstream.
func (m *Mirror) syncRepo(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	start := time.Now()
	if err := sanitizeMirrorNetworkConfig(ctx, repoPath); err != nil {
		return err
	}
	args := []string{
		"-C", repoPath,
		"-c", "gc.auto=0",
		"-c", "core.compression=0",
		"-c", "pack.window=0",
		"-c", "pack.depth=0",
		"-c", "pack.deltaCacheSize=1",
		"-c", "pack.threads=1",
		"fetch", "--prune", "--force", "--", upstreamURL, "+refs/*:refs/*",
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv(authHeader)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %w\noutput: %s", err, output)
	}
	if err := syncMirrorHEAD(ctx, repoPath, upstreamURL, authHeader); err != nil {
		return err
	}
	m.log.Debug("sync complete", "path", repoPath, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// sanitizeMirrorNetworkConfig removes configuration that an earlier job could
// persist to redirect a later authenticated fetch or execute code while the
// new job's Authorization header is present. Syncs use the caller-supplied
// upstream URL and refspec directly, so persisted remotes are unnecessary.
func sanitizeMirrorNetworkConfig(ctx context.Context, repoPath string) error {
	list := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--local", "--no-includes", "--name-only", "--list")
	list.Env = gitEnv("")
	out, err := list.CombinedOutput()
	if err != nil {
		return fmt.Errorf("list persisted mirror config: %w\noutput: %s", err, out)
	}

	seen := map[string]bool{}
	for _, key := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if key == "" || seen[key] || !unsafePersistedNetworkKey(key) {
			continue
		}
		seen[key] = true
		unset := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--local", "--no-includes", "--unset-all", key)
		unset.Env = gitEnv("")
		if output, err := unset.CombinedOutput(); err != nil {
			return fmt.Errorf("remove unsafe persisted mirror config %s: %w\noutput: %s", key, err, output)
		}
	}
	return nil
}

func unsafePersistedNetworkKey(key string) bool {
	lower := strings.ToLower(key)
	for _, prefix := range []string{
		"credential.",
		"http.",
		"include.",
		"includeif.",
		"protocol.",
		"remote.",
		"url.",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return lower == "core.hookspath" || lower == "core.sshcommand"
}

func syncMirrorHEAD(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", upstreamURL, "HEAD")
	cmd.Env = gitEnv(authHeader)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolve upstream HEAD: %w\noutput: %s", err, out)
	}
	var headRef string
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			headRef = fields[1]
			break
		}
	}
	if !strings.HasPrefix(headRef, "refs/heads/") {
		return fmt.Errorf("resolve upstream HEAD: no branch symref in output")
	}
	if out, err := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", headRef).CombinedOutput(); err != nil {
		return fmt.Errorf("update mirror HEAD to %s: %w\noutput: %s", headRef, err, out)
	}
	return nil
}

// HasObject reports whether the mirror repo contains the given object.
func (m *Mirror) HasObject(ctx context.Context, repoPath, oid string) bool {
	return exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "-e", oid).Run() == nil
}

// IsUsable reports whether repoPath is a valid bare repository. A failed
// info/refs mirror clone is followed by a want-less protocol-v2 ls-refs POST,
// so upload-pack must check the repository itself rather than relying only on
// wanted-object checks.
func (m *Mirror) IsUsable(ctx context.Context, repoPath string) bool {
	return IsUsableRepo(ctx, repoPath)
}

func IsUsableRepo(ctx context.Context, repoPath string) bool {
	out, err := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-parse", "--is-bare-repository").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// optimizeRepo runs maintenance tasks: bitmaps and commit-graphs make
// upload-pack on warm jobs fast for large repos, and persist in the snapshot.
// If full is true (after clone), repack with bitmap; otherwise (after sync)
// only midx+commit-graph.
func (m *Mirror) optimizeRepo(ctx context.Context, repoPath string, full bool) {
	// Avoid lock contention if another git process is writing commit-graph.
	if _, err := os.Stat(filepath.Join(repoPath, "objects", "info", "commit-graph.lock")); err == nil {
		return
	}
	if full {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "repack", "-a", "-d", "-b", "--write-bitmap-index")
		if output, err := cmd.CombinedOutput(); err != nil {
			m.log.Warn("git repack failed", "path", repoPath, "err", err, "output", string(output))
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "commit-graph", "write", "--reachable")
	if output, err := cmd.CombinedOutput(); err != nil {
		m.log.Warn("git commit-graph write failed", "path", repoPath, "err", err, "output", string(output))
	}
	cmd = exec.CommandContext(ctx, "git", "-C", repoPath, "multi-pack-index", "write", "--bitmap")
	if output, err := cmd.CombinedOutput(); err != nil {
		m.log.Warn("git multi-pack-index write failed", "path", repoPath, "err", err, "output", string(output))
	}
}

// scheduleOptimize runs optimizeRepo in the background with a per-repo
// singleflight to avoid concurrent maintenance.
func (m *Mirror) scheduleOptimize(repoPath string, full bool) {
	m.maintMu.Lock()
	if m.closed {
		m.maintMu.Unlock()
		return
	}
	m.maintWG.Add(1)
	m.maintMu.Unlock()

	go func() {
		defer m.maintWG.Done()
		m.maintGroup.Do(repoPath, func() (interface{}, error) {
			m.optimizeRepo(m.ctx, repoPath, full)
			return nil, nil
		})
	}()
}

// Close cancels and joins background repository maintenance so no git child
// can keep writing to the sticky disk after the proxy exits for snapshotting.
func (m *Mirror) Close() {
	m.maintMu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.maintMu.Unlock()
	m.maintWG.Wait()
}

// gitEnv returns the environment for git commands talking to upstream. The
// global/system config MUST be masked: once the action has installed its
// url.insteadOf rewrites, an upstream fetch reading the runner's global
// config would loop back into this proxy.
func gitEnv(authHeader string) []string {
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if authHeader != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraheader",
			fmt.Sprintf("GIT_CONFIG_VALUE_0=Authorization: %s", authHeader),
		)
	}
	return env
}
