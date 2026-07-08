package gitproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultStaleAfter keeps mirrors fresh within a job: the first request
	// per repo always syncs (lastSync is in-memory, fresh per process), and
	// anything fetched again more than 2s later re-syncs.
	DefaultStaleAfter = 2 * time.Second

	statusHeader = "X-Git-Proxy-Status"
)

// Options configures the proxy server.
type Options struct {
	// MirrorDir is the mirror storage root (on the sticky disk).
	MirrorDir string
	// AllowedHosts are the upstream hosts the proxy will serve/forward.
	AllowedHosts []string
	// StaleAfter bounds how long a mirror is served without re-syncing.
	StaleAfter time.Duration
	Logger     *slog.Logger
}

// Server routes git smart-HTTP requests: upload-pack traffic is served from
// local mirrors via `git http-backend`, everything else (LFS, receive-pack,
// unknown endpoints) is transparently forwarded upstream so nothing breaks.
type Server struct {
	opts   Options
	mirror *Mirror
	cgi    *cgi.Handler
	log    *slog.Logger

	// upstreamBase maps a host to the scheme+host prefix used for mirror
	// syncs and forwarded requests. Tests override it to point at a fixture
	// server; production always talks https to the real host.
	upstreamBase func(host string) string
}

// NewServer creates the proxy server, verifying git is available.
func NewServer(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = DefaultStaleAfter
	}
	if len(opts.AllowedHosts) == 0 {
		opts.AllowedHosts = []string{"github.com"}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found in PATH: %w", err)
	}
	mirror, err := NewMirror(opts.MirrorDir, opts.StaleAfter, opts.Logger)
	if err != nil {
		return nil, err
	}
	return &Server{
		opts:         opts,
		mirror:       mirror,
		log:          opts.Logger,
		upstreamBase: func(host string) string { return "https://" + host },
		// git http-backend implements the smart-HTTP protocol exactly
		// (including protocol v2). The runner's global/system git config is
		// masked so the action's own insteadOf rewrites never apply here.
		cgi: &cgi.Handler{
			Path: gitPath,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + opts.MirrorDir,
				"GIT_HTTP_EXPORT_ALL=1",
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_CONFIG_SYSTEM=/dev/null",
			},
			InheritEnv: []string{"PATH"},
		},
	}, nil
}

// Handler returns the proxy's HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
			return
		}

		target, err := s.resolveTarget(r)
		if err != nil {
			s.log.Error("resolve target failed", "path", r.URL.Path, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch target.kind {
		case kindInfoRefs:
			s.handleInfoRefs(w, r, target)
		case kindUploadPack:
			s.handleUploadPack(w, r, target)
		default:
			// LFS batch/locks, receive-pack, dumb-protocol paths, anything
			// else: forward upstream verbatim so nothing breaks.
			s.forwardUpstream(w, r, target, r.Body, "unhandled-endpoint")
		}
	})
}

type requestKind int

const (
	kindInfoRefs requestKind = iota
	kindUploadPack
	kindFallback
)

// target describes a parsed request path /{host}/{owner}/{repo}[.git]/<rest>.
type target struct {
	kind        requestKind
	host        string
	owner, repo string
	// rest is the path after /{host}, with the original repo spelling,
	// used when forwarding upstream.
	rest string
}

func (t target) repoKey() string {
	return fmt.Sprintf("%s/%s/%s", t.host, t.owner, t.repo)
}

// cgiPath is the canonical path handed to git http-backend, matching the
// on-disk mirror layout <root>/{host}/{owner}/{repo}.git.
func (t target) cgiPath(suffix string) string {
	return "/" + t.host + "/" + t.owner + "/" + t.repo + ".git" + suffix
}

func (s *Server) resolveTarget(r *http.Request) (target, error) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return target{}, fmt.Errorf("invalid path %q: expected /{host}/{owner}/{repo}/...", r.URL.Path)
	}
	t := target{host: parts[0], rest: "/" + parts[1]}
	if !slices.Contains(s.opts.AllowedHosts, t.host) {
		return target{}, fmt.Errorf("upstream %q not in allowed list", t.host)
	}

	repoPath := t.rest
	switch {
	case strings.HasSuffix(repoPath, "/info/refs") && r.Method == http.MethodGet && r.URL.Query().Get("service") == "git-upload-pack":
		t.kind = kindInfoRefs
		repoPath = strings.TrimSuffix(repoPath, "/info/refs")
	case strings.HasSuffix(repoPath, "/git-upload-pack") && r.Method == http.MethodPost:
		t.kind = kindUploadPack
		repoPath = strings.TrimSuffix(repoPath, "/git-upload-pack")
	default:
		t.kind = kindFallback
		return t, nil
	}

	repoPath = strings.TrimSuffix(strings.TrimPrefix(repoPath, "/"), ".git")
	segs := strings.Split(repoPath, "/")
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return target{}, fmt.Errorf("invalid repo path %q: expected {owner}/{repo}", repoPath)
	}
	t.owner, t.repo = segs[0], segs[1]
	return t, nil
}

// handleInfoRefs syncs the mirror (clone or fetch, auth forwarded from the
// client) then serves the ref advertisement locally. Every git fetch starts
// with info/refs, so this is the once-per-job upstream sync point.
func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, t target) {
	upstreamURL := fmt.Sprintf("%s/%s/%s.git", s.upstreamBase(t.host), t.owner, t.repo)
	repoPath, status, err := s.mirror.EnsureRepo(r.Context(), t.host, t.owner, t.repo, upstreamURL, r.Header.Get("Authorization"))
	if err != nil {
		// Never break a build because mirroring failed: forward the request
		// upstream and let git talk to the real host. Subsequent
		// upload-pack requests fall back too (missing wants, see below).
		s.log.Warn("mirror unavailable, forwarding upstream", "repo", t.repoKey(), "err", err)
		s.forwardUpstream(w, r, t, r.Body, "mirror-error")
		return
	}
	s.log.Info("info/refs", "repo", t.repoKey(), "status", status, "path", repoPath)
	w.Header().Set(statusHeader, string(status))
	s.serveCGI(w, r, t.cgiPath("/info/refs"))
}

// serveCGI hands the request to git http-backend at the canonical repo path.
func (s *Server) serveCGI(w http.ResponseWriter, r *http.Request, canonicalPath string) {
	req := r.Clone(r.Context())
	req.URL.Path = canonicalPath
	s.cgi.ServeHTTP(w, req)
}

// RunFromEnv builds a Server from RUNS_ON_GIT_PROXY_* environment variables
// and serves it until the context is cancelled or a termination signal
// arrives. Used by the hidden --git-proxy-serve entrypoint.
func RunFromEnv(ctx context.Context) error {
	return runFromEnv(ctx)
}
