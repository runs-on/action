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
	"runtime"
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

func assertCanonicalMirrorConfig(t *testing.T, repoPath string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(repoPath, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != canonicalMirrorConfig {
		t.Fatalf("mirror config was not replaced wholesale:\n%s", got)
	}
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
		MirrorDir:   t.TempDir(),
		HealthToken: "test-health-token",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
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
		rest                string
		wantErr             bool
	}{
		{"GET", "/github.com/foo/bar/info/refs", "service=git-upload-pack", kindInfoRefs, "foo", "bar", "/foo/bar/info/refs", false},
		{"GET", "/github.com/Foo/BaR.git/info/refs", "service=git-upload-pack", kindInfoRefs, "foo", "bar", "/Foo/BaR.git/info/refs", false},
		{"POST", "/github.com/foo/bar.git/git-upload-pack", "", kindUploadPack, "foo", "bar", "/foo/bar.git/git-upload-pack", false},
		// receive-pack and LFS endpoints fall through to upstream
		{"GET", "/github.com/foo/bar/info/refs", "service=git-receive-pack", kindFallback, "", "", "/foo/bar/info/refs", false},
		{"POST", "/github.com/foo/bar.git/git-receive-pack", "", kindFallback, "", "", "/foo/bar.git/git-receive-pack", false},
		{"POST", "/github.com/foo/bar.git/info/lfs/objects/batch", "", kindFallback, "", "", "/foo/bar.git/info/lfs/objects/batch", false},
		{"GET", "/gitlab.com/foo/bar/info/refs", "service=git-upload-pack", 0, "", "", "", true},
		{"GET", "/github.com", "", 0, "", "", "", true},
		{"GET", "/github.com/../repo/info/refs", "service=git-upload-pack", 0, "", "", "", true},
		{"GET", "/github.com/owner/../info/refs", "service=git-upload-pack", 0, "", "", "", true},
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
		if target.kind != tt.kind || target.owner != tt.owner || target.repo != tt.repo || target.rest != tt.rest {
			t.Errorf("%s %s: got kind=%d owner=%q repo=%q rest=%q, want kind=%d owner=%q repo=%q rest=%q",
				tt.method, tt.path, target.kind, target.owner, target.repo, target.rest, tt.kind, tt.owner, tt.repo, tt.rest)
		}
	}
}

func TestHealthCheckRequiresActionToken(t *testing.T) {
	server := newTestServer(t, "")
	for _, tt := range []struct {
		name, token string
		status      int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "wrong", status: http.StatusUnauthorized},
		{name: "matching", token: "test-health-token", status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if tt.token != "" {
				req.Header.Set(HealthTokenHeader, tt.token)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("health status = %d, want %d", rec.Code, tt.status)
			}
		})
	}
}

func TestStateFileRoundTripIncludesOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{Port: 8123, PID: 456, MirrorDir: "/mnt/sticky/git/mirrors", Owner: "123/1/build"}
	if err := writeStateFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}

func TestWriteStateFileDoesNotFollowPredictableTempSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows runners")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}

	want := State{Port: 8123, PID: 456, MirrorDir: "/mnt/sticky/git/mirrors", Owner: "123/1/build"}
	if err := writeStateFile(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "untouched" {
		t.Fatalf("predictable temp symlink target = %q, err = %v", got, err)
	}
}

func TestWriteStateFileRejectsSymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows runners")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	err := writeStateFile(filepath.Join(linkDir, "state.json"), State{Port: 8123})
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("writeStateFile through symlinked directory error = %v", err)
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

	// A cancelled clone can leave a directory that exists but is not a bare
	// repository. The next job must replace it instead of persisting a
	// permanently unusable cache entry.
	partialPath := mirror.RepoPath("github.com", "owner", "repo")
	if err := os.MkdirAll(partialPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialPath, "partial"), []byte("incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoPath, status, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "")
	if err != nil || status != StatusClone {
		t.Fatalf("first EnsureRepo: status=%s err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "partial")); !os.IsNotExist(err) {
		t.Fatalf("partial mirror content survived reclone: %v", err)
	}

	// The clone must allow exact-SHA and filtered fetches: actions/checkout
	// fetches commit SHAs, sparse checkouts use --filter.
	for _, key := range []string{"uploadpack.allowanysha1inwant", "uploadpack.allowfilter"} {
		out := gitCmd(t, repoPath, "config", key)
		if strings.TrimSpace(out) != "true" {
			t.Errorf("%s = %q, want true", key, out)
		}
	}

	for _, key := range []string{"uploadpack.allowanysha1inwant", "uploadpack.allowfilter"} {
		gitCmd(t, repoPath, "config", "--unset-all", key)
	}
	mirror.lastSync.Delete("github.com/owner/repo")
	if _, _, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); err != nil {
		t.Fatalf("repair restored mirror settings: %v", err)
	}
	for _, key := range []string{"uploadpack.allowanysha1inwant", "uploadpack.allowfilter"} {
		if got := strings.TrimSpace(gitCmd(t, repoPath, "config", key)); got != "true" {
			t.Fatalf("repaired %s = %q, want true", key, got)
		}
	}

	if _, status, _ = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); status != StatusHit {
		t.Errorf("fresh EnsureRepo: status=%s, want %s", status, StatusHit)
	}

	time.Sleep(60 * time.Millisecond)
	if _, status, _ = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); status != StatusSync {
		t.Errorf("stale EnsureRepo: status=%s, want %s", status, StatusSync)
	}

	upstreamRepo := filepath.Join(strings.TrimPrefix(upstreamBase, "file://"), "owner", "repo.git")
	gitCmd(t, t.TempDir(), "--git-dir", upstreamRepo, "branch", "trunk", headSHA)
	gitCmd(t, t.TempDir(), "--git-dir", upstreamRepo, "symbolic-ref", "HEAD", "refs/heads/trunk")
	time.Sleep(60 * time.Millisecond)
	if _, status, err = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); err != nil || status != StatusSync {
		t.Fatalf("default branch sync: status=%s err=%v", status, err)
	}
	if got := strings.TrimSpace(gitCmd(t, t.TempDir(), "--git-dir", repoPath, "symbolic-ref", "HEAD")); got != "refs/heads/trunk" {
		t.Fatalf("mirror HEAD = %q, want refs/heads/trunk", got)
	}

	if !mirror.HasObject(ctx, repoPath, headSHA) {
		t.Errorf("HasObject(%s) = false, want true", headSHA)
	}
	if mirror.HasObject(ctx, repoPath, strings.Repeat("0", 40)) {
		t.Errorf("HasObject(zeros) = true, want false")
	}

	// An upstream sync failure must reach the HTTP handler so it can forward the
	// ref advertisement upstream instead of serving stale public refs.
	brokenUpstreamURL := "file:///missing-upstream"
	time.Sleep(60 * time.Millisecond)
	if _, status, err = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", brokenUpstreamURL, ""); err == nil || status != "" {
		t.Fatalf("broken public mirror sync: status=%s err=%v, want propagated error", status, err)
	}
	if !mirror.SyncFailed("github.com", "owner", "repo") {
		t.Fatal("public mirror sync failure was not retained for protocol-v2 follow-ups")
	}

	// Private mirrors need the same protocol-v2 fallback. The info/refs
	// request reports an authentication-shaped error, but the following
	// want-less ls-refs request must still stay upstream.
	mirror.syncFailed.Delete("github.com/owner/repo")
	if err := os.WriteFile(filepath.Join(repoPath, ".requires-auth"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, status, err = mirror.EnsureRepo(ctx, "github.com", "owner", "repo", brokenUpstreamURL, "Basic test"); err == nil || status != "" {
		t.Fatalf("broken private mirror sync: status=%s err=%v, want propagated error", status, err)
	}
	if !mirror.SyncFailed("github.com", "owner", "repo") {
		t.Fatal("private mirror sync failure was not retained for protocol-v2 follow-ups")
	}
}

func TestNewMirrorRejectsSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	parent := t.TempDir()
	outside := t.TempDir()
	root := filepath.Join(parent, "mirrors")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewMirror(root, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlinked mirror root error = %v", err)
	}
}

func TestMirrorRejectsSymlinkedParentBeforeClone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	upstreamBase, _ := newUpstreamRepo(t)
	root := t.TempDir()
	mirror, err := NewMirror(root, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "github.com")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "Basic privileged"); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlinked mirror parent error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "owner")); !os.IsNotExist(err) {
		t.Fatalf("authenticated clone wrote through symlinked parent: %v", err)
	}
}

func TestMirrorReclonesBrokenReachableObjectGraph(t *testing.T) {
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath, _, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	packs, err := filepath.Glob(filepath.Join(repoPath, "objects", "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("mirror packs = %v, err = %v", packs, err)
	}
	if err := os.Remove(packs[0]); err != nil {
		t.Fatal(err)
	}
	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror with a broken reachable object graph was accepted")
	}

	mirror.lastSync.Delete("github.com/owner/repo")
	if _, status, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", ""); err != nil || status != StatusClone {
		t.Fatalf("repair broken mirror: status=%s err=%v", status, err)
	}
}

func TestMirrorReclonesSymlinkedObjectStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath, _, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	outsideObjects := filepath.Join(t.TempDir(), "objects")
	if err := os.Rename(filepath.Join(repoPath, "objects"), outsideObjects); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideObjects, filepath.Join(repoPath, "objects")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror with a symlinked object store was accepted")
	}

	mirror.lastSync.Delete("github.com/owner/repo")
	if _, status, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", ""); err != nil || status != StatusClone {
		t.Fatalf("repair symlinked object store: status=%s err=%v", status, err)
	}
	if _, err := os.Stat(outsideObjects); err != nil {
		t.Fatalf("repair followed and removed the external object store: %v", err)
	}
}

func TestMirrorReclonesSymlinkedRefsHierarchy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath, _, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	outsideRefs := filepath.Join(t.TempDir(), "heads")
	if err := os.Rename(filepath.Join(repoPath, "refs", "heads"), outsideRefs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRefs, filepath.Join(repoPath, "refs", "heads")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror with a symlinked refs hierarchy was accepted")
	}

	mirror.lastSync.Delete("github.com/owner/repo")
	if _, status, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", ""); err != nil || status != StatusClone {
		t.Fatalf("repair symlinked refs hierarchy: status=%s err=%v", status, err)
	}
	if _, err := os.Stat(outsideRefs); err != nil {
		t.Fatalf("repair followed and removed the external refs directory: %v", err)
	}

	// The walk is not a fixed-path list: a symlink planted deeper in the
	// hierarchy must be rejected the same way.
	repoPath = mirror.RepoPath("github.com", "owner", "repo")
	nested := filepath.Join(repoPath, "refs", "heads", "feature")
	if err := os.Symlink(t.TempDir(), nested); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror with a nested refs symlink was accepted")
	}
}

func TestAlternatesCleanupDoesNotFollowSymlinkedAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	// A restored snapshot plants objects/info as a symlink to a runner
	// directory containing a file named "alternates". Storage validation must
	// reject the mirror before the alternates cleanup can resolve through the
	// ancestor and delete the external file.
	outside := t.TempDir()
	external := filepath.Join(outside, "alternates")
	if err := os.WriteFile(external, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoPath := mirror.RepoPath("github.com", "owner", "repo")
	if err := os.MkdirAll(filepath.Join(repoPath, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repoPath, "objects", "info")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror with a symlinked objects/info ancestor was accepted")
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "keep" {
		t.Fatalf("alternates cleanup escaped through the symlinked ancestor: data=%q err=%v", data, err)
	}
}

func TestAuthenticatedSyncReplacesPersistedConfig(t *testing.T) {
	upstreamBase, headSHA := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	ctx := t.Context()
	upstreamURL := upstreamBase + "/owner/repo.git"
	repoPath, _, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "")
	if err != nil {
		t.Fatal(err)
	}
	mirror.maintWG.Wait()

	attackerRequests := make(chan string, 10)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerRequests <- r.Header.Get("Authorization")
		http.Error(w, "unexpected request", http.StatusUnauthorized)
	}))
	defer attacker.Close()

	// Every network-affecting value below is runner-writable persisted state
	// from the previous job. None may influence the authenticated sync.
	gitCmd(t, repoPath, "config", "remote.origin.url", attacker.URL+"/origin.git")
	gitCmd(t, repoPath, "remote", "add", "attacker", attacker.URL+"/extra.git")
	gitCmd(t, repoPath, "config", "url."+attacker.URL+"/.insteadOf", upstreamURL)
	gitCmd(t, repoPath, "config", "http.extraHeader", "Authorization: persisted")
	gitCmd(t, repoPath, "config", "core.hooksPath", filepath.Join(repoPath, "hooks-from-earlier-job"))
	gitCmd(t, repoPath, "config", "core.alternateRefsCommand", "false")
	gitCmd(t, repoPath, "config", "fetch.bundleURI", attacker.URL+"/bundle")
	gitCmd(t, repoPath, "config", "bundle.seed.uri", attacker.URL+"/seed")
	gitCmd(t, repoPath, "config", "uploadpack.hideRefs", "refs/heads/main")
	gitCmd(t, repoPath, "config", "transfer.hideRefs", "refs/heads/main")
	gitCmd(t, repoPath, "config", "uploadpack.packObjectsHook", "false")
	gitCmd(t, repoPath, "config", "future.attackerControlledSetting", "must-not-survive")

	exfiltratedToken := filepath.Join(t.TempDir(), "exfiltrated-token")
	hook := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$GIT_CONFIG_VALUE_0\" > %q\n", exfiltratedToken)
	if err := os.WriteFile(filepath.Join(repoPath, "hooks", "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	upstreamRepo := filepath.Join(strings.TrimPrefix(upstreamBase, "file://"), "owner", "repo.git")
	gitCmd(t, t.TempDir(), "--git-dir", upstreamRepo, "branch", "post-clone", headSHA)

	mirror.lastSync.Delete("github.com/owner/repo")
	if _, status, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "Basic privileged"); err != nil || status != StatusSync {
		t.Fatalf("authenticated sync: status=%s err=%v", status, err)
	}
	select {
	case header := <-attackerRequests:
		t.Fatalf("persisted network config received an upstream request with Authorization %q", header)
	default:
	}
	if token, err := os.ReadFile(exfiltratedToken); err == nil {
		t.Fatalf("persisted reference-transaction hook received %q", token)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	assertCanonicalMirrorConfig(t, repoPath)
}

func TestPrepareForServeReplacesPersistedConfig(t *testing.T) {
	root := t.TempDir()
	mirror, err := NewMirror(root, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath := mirror.RepoPath("github.com", "owner", "repo")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, t.TempDir(), "init", "--bare", repoPath)
	keys := []string{
		"fetch.bundleURI",
		"bundle.seed.uri",
		"uploadpack.hideRefs",
		"transfer.hideRefs",
		"uploadpack.packObjectsHook",
		"future.attackerControlledSetting",
	}
	for _, key := range keys {
		gitCmd(t, repoPath, "config", "--add", key, "refs/hidden")
	}

	if err := mirror.PrepareForServe(t.Context(), "github.com", "owner", "repo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mirror.validated.Load("github.com/owner/repo"); !ok {
		t.Fatal("prepared mirror was not cached as validated")
	}
	assertCanonicalMirrorConfig(t, repoPath)
}

func TestMirrorRepairFailureReclonesPersistedRepository(t *testing.T) {
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	ctx := t.Context()
	upstreamURL := upstreamBase + "/owner/repo.git"
	repoPath, _, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "")
	if err != nil {
		t.Fatal(err)
	}

	mirror.Close()
	if err := os.Remove(filepath.Join(repoPath, "config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoPath, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "config", "blocked"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mirror, err = NewMirror(mirror.root, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()
	if _, status, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, ""); err != nil || status != StatusClone {
		t.Fatalf("repair through reclone: status=%s err=%v", status, err)
	}
	if mirror.SyncFailed("github.com", "owner", "repo") {
		t.Fatal("successful repair left protocol-v2 follow-ups pinned upstream")
	}
}

func TestPrivateMirrorMarkerFailureRemovesClone(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo.git")
	// A non-empty directory cannot be removed by the marker's fresh-write
	// path, so the write fails and the fail-closed clone removal must run.
	// (An empty directory or planted symlink is simply replaced.)
	if err := os.MkdirAll(filepath.Join(repoPath, ".requires-auth", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "private-object"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := markPrivateMirror(repoPath); err == nil {
		t.Fatal("private marker unexpectedly succeeded over a non-empty directory")
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("private clone survived marker failure: %v", err)
	}
}

func TestAuthenticatedSyncMarksMirrorPrivate(t *testing.T) {
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	ctx := t.Context()
	upstreamURL := upstreamBase + "/owner/repo.git"
	repoPath, _, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "")
	if err != nil {
		t.Fatal(err)
	}

	mirror.lastSync.Delete("github.com/owner/repo")
	if _, status, err := mirror.EnsureRepo(ctx, "github.com", "owner", "repo", upstreamURL, "Basic test"); err != nil || status != StatusSync {
		t.Fatalf("authenticated sync: status=%s err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".requires-auth")); err != nil {
		t.Fatalf("authenticated sync did not mark mirror private: %v", err)
	}
}

func TestPrivateCloneMarksBeforeConfigFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX git wrapper to inject the configuration failure")
	}
	upstreamBase, _ := newUpstreamRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "clone" ]; then
    %q "$@"
    status=$?
    if [ "$status" -eq 0 ]; then
      for last in "$@"; do :; done
      rm -f "$last/config"
      mkdir "$last/config"
      printf blocked > "$last/config/entry"
    fi
    exit "$status"
  fi
done
exec %q "$@"
`, realGit, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath := mirror.RepoPath("github.com", "owner", "repo")
	_, _, err = mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "Basic test")
	if err == nil || !strings.Contains(err.Error(), "canonical mirror config") {
		t.Fatalf("private clone configuration error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".requires-auth")); err != nil {
		t.Fatalf("private clone was left unmarked after configuration failure: %v", err)
	}
	if !mirror.SyncFailed("github.com", "owner", "repo") {
		t.Fatal("clone setup failure was not retained for protocol-v2 follow-ups")
	}
}

func TestSharedCloneValidatesWaiterAuthorization(t *testing.T) {
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	key := "github.com/owner/repo"
	repoPath := mirror.RepoPath("github.com", "owner", "repo")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, t.TempDir(), "init", "--bare", repoPath)
	if err := os.WriteFile(filepath.Join(repoPath, ".requires-auth"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _ = mirror.group.Do("clone:"+key, func() (interface{}, error) {
			close(started)
			<-release
			mirror.markAuthorized(key, repoPath, "Basic leader")
			return StatusClone, nil
		})
	}()
	<-started
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()

	_, _, err = mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", "file:///missing-upstream", "Basic waiter")
	<-finished
	if err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("shared clone waiter authorization error = %v", err)
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

	t.Run("missing want in large request forwards upload-pack upstream", func(t *testing.T) {
		requests = nil
		mirrorRepo := server.mirror.RepoPath("github.com", "owner", "repo")
		if err := os.MkdirAll(filepath.Dir(mirrorRepo), 0o755); err != nil {
			t.Fatal(err)
		}
		if !server.mirror.IsUsable(t.Context(), mirrorRepo) {
			gitCmd(t, t.TempDir(), "init", "--bare", mirrorRepo)
		}

		body := pkt("command=fetch") + "0001" + pkt("want "+strings.Repeat("d", 40)+"\n") + strings.Repeat("x", maxParseBody)
		resp, err := http.Post(ts.URL+"/github.com/owner/repo.git/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(statusHeader); got != "upstream-missing-want" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if len(requests) != 1 || requests[0].path != "/owner/repo.git/git-upload-pack" || requests[0].body != body {
			t.Errorf("upstream did not receive the complete large request")
		}
	})

	t.Run("private mirror direct upload-pack authenticates upstream", func(t *testing.T) {
		requests = nil
		mirrorRepo := server.mirror.RepoPath("github.com", "owner", "repo")
		if err := os.MkdirAll(filepath.Dir(mirrorRepo), 0o755); err != nil {
			t.Fatal(err)
		}
		if !server.mirror.IsUsable(t.Context(), mirrorRepo) {
			gitCmd(t, t.TempDir(), "init", "--bare", mirrorRepo)
		}
		authMarker := filepath.Join(mirrorRepo, ".requires-auth")
		if err := os.WriteFile(authMarker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(authMarker)

		lsRefs := pkt("command=ls-refs") + "0001" + pkt("peel\n") + "0000"
		resp, err := http.Post(ts.URL+"/github.com/owner/repo.git/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(lsRefs))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if got := resp.Header.Get(statusHeader); got != "upstream-authentication-required" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if string(body) != "upstream response" {
			t.Errorf("private mirror response came from local cache: %q", body)
		}
	})

	t.Run("failed sync keeps protocol v2 upstream", func(t *testing.T) {
		requests = nil
		server.mirror.syncFailed.Store("github.com/owner/repo", true)
		defer server.mirror.syncFailed.Delete("github.com/owner/repo")

		lsRefs := pkt("command=ls-refs") + "0001" + pkt("peel\n") + "0000"
		resp, err := http.Post(ts.URL+"/github.com/owner/repo.git/git-upload-pack", "application/x-git-upload-pack-request", strings.NewReader(lsRefs))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(statusHeader); got != "upstream-mirror-sync-failed" {
			t.Errorf("%s = %q", statusHeader, got)
		}
		if len(requests) != 1 || requests[0].path != "/owner/repo.git/git-upload-pack" || requests[0].body != lsRefs {
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

// A restored snapshot can replace the repo.git leaf (or any component above
// it) with a symlink to a runner-writable directory. Probe callers reach the
// persisted mirror before EnsureRepo validates it, and replaceMirrorConfig
// must never rename a config file through such a link.
func TestIsUsableRepoRejectsSymlinkedRepoLeaf(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	victimConfig := filepath.Join(victim, "config")
	if err := os.WriteFile(victimConfig, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostDir := filepath.Join(root, "github.com", "owner")
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(hostDir, "repo.git")
	if err := os.Symlink(victim, repoPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if IsUsableRepo(t.Context(), root, repoPath) {
		t.Fatal("mirror behind a symlinked repo leaf was accepted")
	}
	if got, err := os.ReadFile(victimConfig); err != nil || string(got) != "victim" {
		t.Fatalf("victim config was modified: %q, err = %v", got, err)
	}
}

func TestIsUsableRepoRejectsSymlinkedHostDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	repoInOutside := filepath.Join(outside, "owner", "repo.git")
	if err := os.MkdirAll(repoInOutside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "github.com")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if IsUsableRepo(t.Context(), root, filepath.Join(root, "github.com", "owner", "repo.git")) {
		t.Fatal("mirror behind a symlinked host directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(repoInOutside, "config")); !os.IsNotExist(err) {
		t.Fatalf("config was written through the symlinked host directory: %v", err)
	}
}

// The opaque per-job client credential must map to the real GitHub token for
// upstream operations; anything else passes through verbatim so foreign
// clients keep their own permissions.
func TestUpstreamAuthSubstitution(t *testing.T) {
	s := &Server{opts: Options{ClientToken: "opaque", UpstreamToken: "real-pat"}}
	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return r
	}

	if got := s.upstreamAuth(req(BasicAuthHeader("opaque"))); got != BasicAuthHeader("real-pat") {
		t.Fatalf("client credential mapped to %q, want real token", got)
	}
	if got := s.upstreamAuth(req("basic somebody-elses-credential")); got != "basic somebody-elses-credential" {
		t.Fatalf("foreign credential rewritten to %q", got)
	}
	if got := s.upstreamAuth(req("")); got != "" {
		t.Fatalf("missing credential became %q", got)
	}

	noToken := &Server{opts: Options{ClientToken: "opaque"}}
	if got := noToken.upstreamAuth(req(BasicAuthHeader("opaque"))); got != "" {
		t.Fatalf("client credential with no upstream token became %q", got)
	}

	passthrough := &Server{opts: Options{}}
	if got := passthrough.upstreamAuth(req("basic legacy")); got != "basic legacy" {
		t.Fatalf("passthrough server rewrote credential to %q", got)
	}
}

// A restored snapshot can plant a symlink at the marker path; the write must
// replace the link, never create a file at its target.
func TestPrivateMirrorMarkerReplacesPlantedSymlink(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo.git")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "ssh-config")
	marker := filepath.Join(repoPath, ".requires-auth")
	if err := os.Symlink(outside, marker); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := markPrivateMirror(repoPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("marker write followed the planted symlink: %v", err)
	}
	if fi, err := os.Lstat(marker); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("marker is not a regular file: mode=%v err=%v", fi.Mode(), err)
	}
}

func TestReadTokenFrom(t *testing.T) {
	if got := readTokenFrom(strings.NewReader("ghs_secret\n")); got != "ghs_secret" {
		t.Fatalf("token = %q", got)
	}
	if got := readTokenFrom(strings.NewReader("")); got != "" {
		t.Fatalf("empty stream token = %q", got)
	}
	if got := readTokenFrom(strings.NewReader("  \n")); got != "" {
		t.Fatalf("whitespace stream token = %q", got)
	}
}

func TestIsReceivePack(t *testing.T) {
	if !isReceivePack(target{rest: "/foo/bar.git/git-receive-pack"}, httptest.NewRequest(http.MethodPost, "/github.com/foo/bar.git/git-receive-pack", nil)) {
		t.Fatal("receive-pack POST not identified")
	}
	if !isReceivePack(target{rest: "/foo/bar/info/refs"}, httptest.NewRequest(http.MethodGet, "/github.com/foo/bar/info/refs?service=git-receive-pack", nil)) {
		t.Fatal("receive-pack handshake not identified")
	}
	if isReceivePack(target{rest: "/foo/bar.git/info/lfs/objects/batch"}, httptest.NewRequest(http.MethodPost, "/github.com/foo/bar.git/info/lfs/objects/batch", nil)) {
		t.Fatal("LFS batch misidentified as receive-pack")
	}
}

func TestLFSBatchIsDownload(t *testing.T) {
	if !lfsBatchIsDownload([]byte(`{"operation":"download","objects":[]}`)) {
		t.Fatal("download batch not recognized")
	}
	if lfsBatchIsDownload([]byte(`{"operation":"upload","objects":[]}`)) {
		t.Fatal("upload batch treated as download")
	}
	if lfsBatchIsDownload([]byte(`not json`)) {
		t.Fatal("unparseable body did not fail closed")
	}
}

// A restored mirror must not satisfy usability through objects borrowed from
// an external object store: a background repack would copy them into the
// snapshot, leaking an arbitrary runner-local repository across jobs.
func TestIsUsableRepoRejectsPersistedAlternates(t *testing.T) {
	upstreamBase, _ := newUpstreamRepo(t)
	mirror, err := NewMirror(t.TempDir(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()

	repoPath, _, err := mirror.EnsureRepo(t.Context(), "github.com", "owner", "repo", upstreamBase+"/owner/repo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	upstreamObjects := filepath.Join(strings.TrimPrefix(upstreamBase, "file://"), "owner", "repo.git", "objects")
	alternates := filepath.Join(repoPath, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(upstreamObjects+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	packs, err := filepath.Glob(filepath.Join(repoPath, "objects", "pack", "*"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("mirror packs = %v, err = %v", packs, err)
	}
	for _, pack := range packs {
		if err := os.Remove(pack); err != nil {
			t.Fatal(err)
		}
	}

	if IsUsableRepo(t.Context(), mirror.root, repoPath) {
		t.Fatal("mirror relying on external object alternates was accepted")
	}
	if _, err := os.Lstat(alternates); !os.IsNotExist(err) {
		t.Fatalf("persisted alternates survived canonicalization: %v", err)
	}
}
