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
	"io/fs"
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

	// canonicalMirrorConfig is the complete local configuration for every
	// cached mirror. Restored mirrors are runner-writable state, so their
	// config is replaced wholesale before any git command reads it.
	canonicalMirrorConfig = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
[remote "origin"]
	fetch = +refs/*:refs/*
	mirror = true
[uploadpack]
	allowAnySHA1InWant = true
	allowFilter = true
`
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
	validated  sync.Map // map[repoKey]bool; persisted repo was canonicalized and fscked this job
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
	if err := requireRealDirectory(root); err != nil {
		return nil, fmt.Errorf("validate mirror root: %w", err)
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
		if err := ensureRealSubdirectories(m.root, filepath.Dir(repoPath)); err != nil {
			return StatusClone, fmt.Errorf("validate mirror parent: %w", err)
		}
		info, statErr := os.Lstat(repoPath)
		if statErr == nil && (!info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0) {
			m.log.Warn("removing unsafe persisted mirror path", "repo", key, "path", repoPath)
			if err := os.RemoveAll(repoPath); err != nil {
				return StatusClone, fmt.Errorf("remove unsafe mirror path: %w", err)
			}
			statErr = os.ErrNotExist
		}
		if statErr == nil {
			if _, syncedThisJob := m.lastSync.Load(key); !syncedThisJob {
				// The first info/refs request is the job boundary for a restored
				// mirror. Re-run persisted-state validation even if a direct
				// POST happened to prepare the repo first.
				m.validated.Delete(key)
			}
			if err := m.prepareRepoForServe(ctx, key, repoPath); err != nil {
				m.log.Warn("removing unusable persisted mirror", "repo", key, "path", repoPath, "err", err)
				m.validated.Delete(key)
				if err := os.RemoveAll(repoPath); err != nil {
					return StatusClone, fmt.Errorf("remove unusable mirror: %w", err)
				}
				statErr = os.ErrNotExist
			}
		}
		if statErr == nil {
			// prepareRepoForServe also replaces configuration that a previous
			// job could have changed.
		} else if !os.IsNotExist(statErr) {
			m.log.Warn("removing unusable persisted mirror", "repo", key, "path", repoPath)
			return StatusHit, fmt.Errorf("inspect mirror: %w", statErr)
		}
		if os.IsNotExist(statErr) {
			if err := m.cloneRepo(ctx, repoPath, upstreamURL, authHeader); err != nil {
				// cloneRepo can fail after creating a usable bare repository
				// (for example during upload-pack configuration). Keep every
				// protocol-v2 follow-up upstream until a later setup succeeds.
				if _, statErr := os.Lstat(repoPath); statErr == nil {
					m.syncFailed.Store(key, true)
				}
				return StatusClone, err
			}
			m.markAuthorized(key, repoPath, authHeader)
			m.validated.Store(key, true)
			m.lastSync.Store(key, time.Now())
			return StatusClone, nil
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

// PrepareForServe canonicalizes and validates a persisted mirror before any local
// upload-pack CGI can read it. The expensive connectivity fsck runs once per
// repository per proxy process, not once per protocol-v2 POST.
func (m *Mirror) PrepareForServe(ctx context.Context, host, owner, repo string) error {
	key := fmt.Sprintf("%s/%s/%s", host, owner, repo)
	return m.prepareRepoForServe(ctx, key, m.RepoPath(host, owner, repo))
}

func (m *Mirror) prepareRepoForServe(ctx context.Context, key, repoPath string) error {
	if _, ok := m.validated.Load(key); ok {
		return nil
	}
	_, err, _ := m.group.Do("validate:"+key, func() (interface{}, error) {
		if _, ok := m.validated.Load(key); ok {
			return nil, nil
		}
		// Direct protocol-v2 POSTs reach this path without EnsureRepo, so the
		// component walk from the mirror root must happen here too: a restored
		// symlink at github.com/<owner> would otherwise route the canonical
		// config rewrite into a repository outside the sticky disk.
		if err := validateRealPathWithin(m.root, repoPath); err != nil {
			return nil, err
		}
		if err := validateMirrorStorage(repoPath); err != nil {
			return nil, err
		}
		// Nothing in a mirror's local config is user-authored state worth
		// preserving. Replacing the file closes the entire class of persisted
		// network, command, hook, and serving-policy configuration attacks.
		if err := replaceMirrorConfig(repoPath); err != nil {
			return nil, err
		}
		if !isUsableCanonicalRepo(ctx, repoPath) {
			return nil, fmt.Errorf("mirror is not a usable bare repository")
		}
		m.validated.Store(key, true)
		return nil, nil
	})
	return err
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
	if err := ensureRealSubdirectories(m.root, filepath.Dir(repoPath)); err != nil {
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

	if err := replaceMirrorConfig(repoPath); err != nil {
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
	if err := writeMarkerFresh(marker, []byte("1"), 0o644); err != nil {
		markerErr := fmt.Errorf("mark private mirror %s: %w", repoPath, err)
		if cleanupErr := os.RemoveAll(repoPath); cleanupErr != nil {
			return errors.Join(markerErr, fmt.Errorf("remove unmarked private mirror: %w", cleanupErr))
		}
		return markerErr
	}
	return nil
}

// syncRepo fetches updates from upstream.
func (m *Mirror) syncRepo(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	start := time.Now()
	args := []string{
		"-C", repoPath,
		"-c", "gc.auto=0",
		"-c", "core.compression=0",
		"-c", "pack.window=0",
		"-c", "pack.depth=0",
		"-c", "pack.deltaCacheSize=1",
		"-c", "pack.threads=1",
		// The bare repository is persisted runner-writable state. A previous
		// job can place executable hooks in its default hooks directory even
		// after unsafe config keys are removed.
		"-c", "core.hooksPath=/dev/null",
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

// replaceMirrorConfig atomically replaces attacker-controlled persisted config
// instead of trying to enumerate unsafe keys. The remove-and-retry path is for
// platforms whose rename cannot replace an existing file.
func replaceMirrorConfig(repoPath string) error {
	// Persisted alternates route object lookups — and the objects that fsck,
	// upload-pack, and background repacks read or copy — to arbitrary local
	// repositories; git's connectivity check follows them, so they must be
	// gone before any git invocation touches the repository.
	for _, name := range []string{"alternates", "http-alternates"} {
		path := filepath.Join(repoPath, "objects", "info", name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove persisted object alternates %s: %w", path, err)
		}
	}

	temp, err := os.CreateTemp(repoPath, ".runs-on-config-*")
	if err != nil {
		return fmt.Errorf("create canonical mirror config: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	if err := temp.Chmod(0o644); err != nil {
		return fmt.Errorf("set canonical mirror config permissions: %w", err)
	}
	if _, err := temp.WriteString(canonicalMirrorConfig); err != nil {
		return fmt.Errorf("write canonical mirror config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close canonical mirror config: %w", err)
	}

	configPath := filepath.Join(repoPath, "config")
	if err := os.Rename(tempPath, configPath); err == nil {
		return nil
	} else if removeErr := os.Remove(configPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return errors.Join(
			fmt.Errorf("replace canonical mirror config: %w", err),
			fmt.Errorf("remove persisted mirror config: %w", removeErr),
		)
	}
	if err := os.Rename(tempPath, configPath); err != nil {
		return fmt.Errorf("install canonical mirror config: %w", err)
	}
	return nil
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
	if out, err := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "--git-dir", repoPath, "symbolic-ref", "HEAD", headRef).CombinedOutput(); err != nil {
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
	return IsUsableRepo(ctx, m.root, repoPath)
}

// IsUsableRepo validates every path component of repoPath below root before
// touching the repository: probe callers (cache-hit detection, direct
// upload-pack POSTs) reach persisted state before EnsureRepo has rejected
// restored symlinks. The full storage walk must precede replaceMirrorConfig so
// its alternates removal can never resolve through a planted symlink ancestor.
func IsUsableRepo(ctx context.Context, root, repoPath string) bool {
	if err := validateRealPathWithin(root, repoPath); err != nil {
		return false
	}
	if err := validateMirrorStorage(repoPath); err != nil {
		return false
	}
	if err := replaceMirrorConfig(repoPath); err != nil {
		return false
	}
	return isUsableCanonicalRepo(ctx, repoPath)
}

// isUsableCanonicalRepo requires that the caller already passed repoPath
// through validateMirrorStorage and replaceMirrorConfig.
func isUsableCanonicalRepo(ctx context.Context, repoPath string) bool {
	revParse := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "--git-dir", repoPath, "rev-parse", "--is-bare-repository")
	revParse.Env = gitEnv("")
	out, err := revParse.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "--git-dir", repoPath, "fsck", "--connectivity-only", "--no-dangling", "--no-reflogs")
	cmd.Env = gitEnv("")
	return cmd.Run() == nil
}

// validateMirrorStorage walks the entire persisted repository and rejects any
// entry that is not a real directory or regular file. A restored snapshot is
// runner-writable state: a symlinked directory anywhere in the tree (objects,
// refs/heads, ...) would let a later git ref write, alternates removal, or
// repack escape the sticky disk, so the whole class is rejected in one pass
// instead of enumerating known-sensitive paths one review round at a time.
func validateMirrorStorage(repoPath string) error {
	if err := requireRealDirectory(repoPath); err != nil {
		return err
	}
	return filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("inspect mirror entry %s: %w", path, err)
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("mirror entry %s is not a real directory or regular file", path)
		}
		return nil
	})
}

func ensureRealSubdirectories(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s is outside mirror root %s", target, root)
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create directory %s: %w", current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect directory %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("mirror path component %s is not a real directory", current)
		}
	}
	return nil
}

// validateRealPathWithin verifies that target and every component between
// root and target are real directories (no symlinks, junctions, or files),
// without creating anything. Unlike ensureRealSubdirectories it is safe for
// read-only probes of persisted state.
func validateRealPathWithin(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s is outside mirror root %s", target, root)
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := requireRealDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

// writeMarkerFresh replaces path with a newly created regular file, never
// writing through a pre-existing file or symlink: a restored snapshot could
// plant a symlink at the marker path to redirect the write off the disk.
func writeMarkerFresh(path string, data []byte, perm os.FileMode) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
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
		// Keep unreachable objects in the new pack: actions/checkout commonly
		// fetches the event's exact SHA, which can become orphaned by a
		// force-push while background maintenance is running.
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "repack", "-A", "-d", "-b", "--write-bitmap-index")
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
