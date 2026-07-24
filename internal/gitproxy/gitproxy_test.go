package gitproxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitCmd runs a git command with the user's config masked, failing the test
// on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newUpstreamRepo creates a repo with one commit under dir/{owner}/{repo}.git
// and returns its file:// base and head SHA.
func newUpstreamRepo(t *testing.T) (base string, headSHA string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "init", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "initial")
	headSHA = strings.TrimSpace(gitCmd(t, work, "rev-parse", "HEAD"))

	repoDir := filepath.Join(dir, "owner", "repo.git")
	gitCmd(t, dir, "clone", "--bare", work, repoDir)
	return "file://" + dir, headSHA
}

func newTestServer(t *testing.T, upstreamBase string) *Server {
	t.Helper()
	server, err := NewServer(Options{
		MirrorDir: t.TempDir(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if upstreamBase != "" {
		server.upstreamBase = func(host string) string { return upstreamBase }
	}
	return server
}

func TestResolveTarget(t *testing.T) {
	server := newTestServer(t, "")
	tests := []struct {
		method, path, query string
		kind                requestKind
		owner, repo         string
		wantErr             bool
	}{
		{"GET", "/github.com/foo/bar/info/refs", "service=git-upload-pack", kindInfoRefs, "foo", "bar", false},
		{"GET", "/github.com/foo/bar.git/info/refs", "service=git-upload-pack", kindInfoRefs, "foo", "bar", false},
		{"POST", "/github.com/foo/bar.git/git-upload-pack", "", kindUploadPack, "foo", "bar", false},
		// receive-pack and LFS endpoints fall through to upstream
		{"GET", "/github.com/foo/bar/info/refs", "service=git-receive-pack", kindFallback, "", "", false},
		{"POST", "/github.com/foo/bar.git/git-receive-pack", "", kindFallback, "", "", false},
		{"POST", "/github.com/foo/bar.git/info/lfs/objects/batch", "", kindFallback, "", "", false},
		{"GET", "/gitlab.com/foo/bar/info/refs", "service=git-upload-pack", 0, "", "", true},
		{"GET", "/github.com", "", 0, "", "", true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, tt.path+"?"+tt.query, nil)
		target, err := server.resolveTarget(r)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s %s: expected error", tt.method, tt.path)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %s: unexpected error: %v", tt.method, tt.path, err)
			continue
		}
		if target.kind != tt.kind || target.owner != tt.owner || target.repo != tt.repo {
			t.Errorf("%s %s: got kind=%d owner=%q repo=%q, want kind=%d owner=%q repo=%q",
				tt.method, tt.path, target.kind, target.owner, target.repo, tt.kind, tt.owner, tt.repo)
		}
	}
}

// pkt builds a pkt-line for the given payload.
func pkt(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

func TestParseWants(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha2 := strings.Repeat("b", 40)

	// Protocol v0/v1 framing: wants, flush, done.
	v0 := pkt("want "+sha1+" multi_ack_detailed side-band-64k\n") + pkt("want "+sha2+"\n") + pkt("deepen 1") + "0000" + pkt("done\n")
	if got := parseWants([]byte(v0), ""); len(got) != 2 || got[0] != sha1 || got[1] != sha2 {
		t.Errorf("v0 wants = %v", got)
	}

	// Protocol v2 framing: wants come after a delim-pkt (0001), which must
	// not terminate parsing.
	v2 := pkt("command=fetch") + pkt("agent=git/2.51") + "0001" + pkt("thin-pack\n") + pkt("want "+sha1+"\n") + pkt("deepen 1\n") + "0000"
	if got := parseWants([]byte(v2), ""); len(got) != 1 || got[0] != sha1 {
		t.Errorf("v2 wants = %v", got)
	}

	// v2 ls-refs command has no wants: served locally.
	lsRefs := pkt("command=ls-refs") + "0001" + pkt("peel\n") + pkt("ref-prefix refs/heads/\n") + "0000"
	if got := parseWants([]byte(lsRefs), ""); len(got) != 0 {
		t.Errorf("ls-refs wants = %v", got)
	}

	// Gzip-compressed body.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(v2))
	gz.Close()
	if got := parseWants(buf.Bytes(), "gzip"); len(got) != 1 || got[0] != sha1 {
		t.Errorf("gzip wants = %v", got)
	}

	// Malformed input parses to nothing without panicking.
	if got := parseWants([]byte("zzzz-not-pkt-lines"), ""); len(got) != 0 {
		t.Errorf("malformed wants = %v", got)
	}
}

func TestMirrorEnsureRepo(t *testing.T) {
	upstreamBase, headSHA := newUpstreamRepo(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mirror, err := NewMirror(t.TempDir(), 50*time.Millisecond, log)
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()
	upstreamURL := upstreamBase + "/owner/repo.git"
	ctx := t.Context()

	repoPath, status, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "")
	if err != nil || status != StatusClone {
		t.Fatalf("first EnsureRepo: status=%s err=%v", status, err)
	}

	// The clone must allow exact-SHA and filtered fetches: actions/checkout
	// fetches commit SHAs, sparse checkouts use --filter.
	for _, key := range []string{"uploadpack.allowanysha1inwant", "uploadpack.allowfilter"} {
		out := gitCmd(t, repoPath, "config", key)
		if strings.TrimSpace(out) != "true" {
			t.Errorf("%s = %q, want true", key, out)
		}
	}

	if _, status, _ = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); status != StatusHit {
		t.Errorf("fresh EnsureRepo: status=%s, want %s", status, StatusHit)
	}

	time.Sleep(60 * time.Millisecond)
	if _, status, _ = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); status != StatusSync {
		t.Errorf("stale EnsureRepo: status=%s, want %s", status, StatusSync)
	}

	if !mirror.HasObject(ctx, repoPath, headSHA) {
		t.Errorf("HasObject(%s) = false, want true", headSHA)
	}
	if mirror.HasObject(ctx, repoPath, strings.Repeat("0", 40)) {
		t.Errorf("HasObject(zeros) = true, want false")
	}
}

// TestServerServesGitClients exercises the full stack (router → mirror →
// git http-backend CGI) with real git clients: full clone, shallow clone,
// exact-SHA fetch and filtered fetch, over smart HTTP protocol v2.
func TestServerServesGitClients(t *testing.T) {
	upstreamBase, headSHA := newUpstreamRepo(t)
	server := newTestServer(t, upstreamBase)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	repoURL := ts.URL + "/github.com/owner/repo"

	t.Run("full clone", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		gitCmd(t, t.TempDir(), "clone", repoURL, dst)
		if data, err := os.ReadFile(filepath.Join(dst, "README.md")); err != nil || string(data) != "hello\n" {
			t.Errorf("README.md = %q, err=%v", data, err)
		}
	})

	t.Run("shallow clone", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		gitCmd(t, t.TempDir(), "clone", "--depth", "1", repoURL, dst)
		if sha := strings.TrimSpace(gitCmd(t, dst, "rev-parse", "HEAD")); sha != headSHA {
			t.Errorf("HEAD = %s, want %s", sha, headSHA)
		}
	})

	t.Run("exact SHA fetch like actions/checkout", func(t *testing.T) {
		dst := t.TempDir()
		gitCmd(t, dst, "init", ".")
		gitCmd(t, dst, "remote", "add", "origin", repoURL)
		gitCmd(t, dst, "fetch", "--depth", "1", "origin", "+"+headSHA+":refs/remotes/origin/main")
	})

	t.Run("filtered fetch", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		gitCmd(t, t.TempDir(), "clone", "--filter=blob:none", repoURL, dst)
	})

	t.Run("status header", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/github.com/owner/repo/info/refs?service=git-upload-pack")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		status := resp.Header.Get(statusHeader)
		if !strings.HasPrefix(status, "mirror-") {
			t.Errorf("%s = %q, want mirror-*", statusHeader, status)
		}
	})
}

// TestServerFallbacks verifies that anything the mirror cannot serve is
// transparently forwarded upstream with auth and body intact.
func TestServerFallbacks(t *testing.T) {
	type recorded struct {
		method, path, auth, body string
	}
	var requests []recorded
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body)})
		w.Header().Set("X-Upstream", "yes")
		// The "missing" repo 404s like a real host would, so the proxy's own
		// mirror clone fails rather than dumb-http-cloning an empty repo.
		if strings.Contains(r.URL.Path, "/missing/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "upstream response")
	}))
	defer upstream.Close()

	server := newTestServer(t, upstream.URL)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	t.Run("LFS batch endpoint", func(t *testing.T) {
		requests = nil
		req, _ := http.NewRequest("POST", ts.URL+"/github.com/owner/repo.git/info/lfs/objects/batch", strings.NewReader(`{"operation":"download"}`))
		req.Header.Set("Authorization", "Basic abc123")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "upstream response" || resp.Header.Get("X-Upstream") != "yes" {
			t.Errorf("response not passed through: %q", body)
		}
		if got := resp.Header.Get(statusHeader); got != "upstream-unhandled-endpoint" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if len(requests) != 1 || requests[0].path != "/owner/repo.git/info/lfs/objects/batch" ||
			requests[0].auth != "Basic abc123" || requests[0].body != `{"operation":"download"}` {
			t.Errorf("upstream saw %+v", requests)
		}
	})

	t.Run("missing want forwards upload-pack upstream", func(t *testing.T) {
		requests = nil
		// Seed a mirror so the local path would otherwise be taken.
		mirrorRepo := server.mirror.RepoPath("github.com", "owner", "repo")
		if err := os.MkdirAll(filepath.Dir(mirrorRepo), 0o755); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, t.TempDir(), "init", "--bare", mirrorRepo)

		body := pkt("command=fetch") + "0001" + pkt("want "+strings.Repeat("c", 40)+"\n") + "0000"
		resp, err := http.Post(ts.URL+"/github.com/owner/repo.git/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(statusHeader); got != "upstream-missing-want" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if len(requests) != 1 || requests[0].path != "/owner/repo.git/git-upload-pack" || requests[0].body != body {
			t.Errorf("upstream saw %+v", requests)
		}
	})

	t.Run("mirror clone failure forwards info/refs upstream", func(t *testing.T) {
		requests = nil
		// missing/repo does not exist upstream (the fixture serves HTTP but
		// git clone against it fails), so EnsureRepo errors out.
		resp, err := http.Get(ts.URL + "/github.com/missing/repo/info/refs?service=git-upload-pack")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(statusHeader); got != "upstream-mirror-error" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		// The 404 from upstream passes through untouched.
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		if len(requests) == 0 || requests[len(requests)-1].path != "/missing/repo/info/refs" {
			t.Errorf("upstream saw %+v", requests)
		}

		// Protocol v2 follows info/refs with a want-less ls-refs command. The
		// absent mirror must not be handed to git http-backend.
		requests = nil
		lsRefs := pkt("command=ls-refs") + "0001" + pkt("peel\n") + "0000"
		resp, err = http.Post(ts.URL+"/github.com/missing/repo/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(lsRefs))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(statusHeader); got != "upstream-mirror-unavailable" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if len(requests) != 1 || requests[0].path != "/missing/repo/git-upload-pack" || requests[0].body != lsRefs {
			t.Errorf("upstream saw %+v", requests)
		}
	})
}
