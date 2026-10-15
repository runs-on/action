package mbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runs-on/action/internal/magiccache"
)

const testClientToken = "client-token"

func newTestServer(t *testing.T) (*httptest.Server, *fakeCacheService, *Server) {
	t.Helper()
	cacheSrv, cache := newFakeCacheService(t)
	server := NewServer(magiccache.NewCacheClient(cacheSrv.URL, testRuntimeToken), testClientToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)
	return srv, cache, server
}

// do sends a request as mbx does: protocol and namespace headers and the
// client token.
func do(t *testing.T, method, url string, body []byte, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	req.Header.Set(protocolHeader, "1")
	req.Header.Set(namespaceHeader, "acme/project")
	for name, value := range headers {
		if value == "" {
			req.Header.Del(name)
		} else {
			req.Header.Set(name, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data
}

func hash(c byte) string { return strings.Repeat(string(c), 64) }

func TestRejectsRequestsWithoutTheClientToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, token := range []string{"", "Bearer wrong", "client-token"} {
		status, _, _ := do(t, http.MethodGet, srv.URL+"/v1/capabilities", nil, map[string]string{"Authorization": token})
		if status != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", token, status)
		}
	}
}

func TestAdvertisesTheBaselineAndActionManifests(t *testing.T) {
	srv, _, _ := newTestServer(t)
	status, _, body := do(t, http.MethodGet, srv.URL+"/v1/capabilities", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	var caps struct {
		Protocol struct{ Major int } `json:"protocol"`
		Features map[string]bool     `json:"features"`
		Compress []string            `json:"compressors"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Protocol.Major != 1 || !caps.Features["action_manifests"] || caps.Features["blob_packs"] || caps.Features["action_batch"] || len(caps.Compress) != 0 {
		t.Errorf("capabilities %s", body)
	}
	// Extensions mbx falls back from on a 404.
	for _, path := range []string{"/v1/blobs:pack", "/v1/action-results:batch", "/v1/action-promises/blake3/" + hash('a') + "/1"} {
		if status, _, _ := do(t, http.MethodPost, srv.URL+path, []byte("{}"), nil); status != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404", path, status)
		}
	}
	if status, _, _ := do(t, http.MethodGet, srv.URL+"/v1/capabilities", nil, map[string]string{protocolHeader: "2"}); status != http.StatusBadRequest {
		t.Errorf("protocol 2: status %d, want 400", status)
	}
}

func TestStoresObjectsOnceAndServesThemBack(t *testing.T) {
	srv, cache, server := newTestServer(t)
	data := []byte("compiled output")
	blobURL := fmt.Sprintf("%s/v1/blobs/blake3/%s/%d", srv.URL, hash('b'), len(data))
	if status, _, _ := do(t, http.MethodGet, blobURL, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET before PUT: status %d, want 404", status)
	}
	if status, _, _ := do(t, http.MethodPut, blobURL, data, map[string]string{"If-None-Match": "*"}); status != http.StatusCreated {
		t.Fatalf("PUT: status %d, want 201", status)
	}
	// mbx treats 412 as already stored.
	if status, _, _ := do(t, http.MethodPut, blobURL, data, map[string]string{"If-None-Match": "*"}); status != http.StatusPreconditionFailed {
		t.Fatalf("second PUT: status %d, want 412", status)
	}
	status, header, body := do(t, http.MethodGet, blobURL, nil, nil)
	if status != http.StatusOK || !bytes.Equal(body, data) || header.Get("Content-Type") != blobMedia {
		t.Fatalf("GET: status %d, body %q, type %q", status, body, header.Get("Content-Type"))
	}
	record := []byte(`{"version":1}`)
	resultURL := fmt.Sprintf("%s/v1/action-results/blake3/%s/42", srv.URL, hash('c'))
	if status, _, _ := do(t, http.MethodPut, resultURL, record, map[string]string{"If-None-Match": "*"}); status != http.StatusCreated {
		t.Fatalf("PUT action result: status %d", status)
	}
	if status, _, body := do(t, http.MethodGet, resultURL, nil, nil); status != http.StatusOK || !bytes.Equal(body, record) {
		t.Fatalf("GET action result: status %d, body %q", status, body)
	}
	// Namespaces keep objects apart.
	if status, _, _ := do(t, http.MethodGet, resultURL, nil, map[string]string{namespaceHeader: "other/project"}); status != http.StatusNotFound {
		t.Errorf("another namespace: status %d, want 404", status)
	}
	if status, _, _ := do(t, http.MethodGet, resultURL, nil, map[string]string{namespaceHeader: ""}); status != http.StatusBadRequest {
		t.Errorf("no namespace: status %d, want 400", status)
	}
	stats := server.Stats()
	if stats.Uploads != 2 || stats.Existing != 1 || stats.Hits != 2 || stats.Misses != 2 {
		t.Errorf("stats %+v", stats)
	}
	if cache.entryCount() != 2 {
		t.Errorf("%d cache entries, want 2", cache.entryCount())
	}
}

func TestSpreadsEntriesOverVersionsByHash(t *testing.T) {
	srv, cache, _ := newTestServer(t)
	for _, c := range "0123456789abcdef" {
		url := fmt.Sprintf("%s/v1/blobs/sha256/%s/1", srv.URL, hash(byte(c)))
		if status, _, _ := do(t, http.MethodPut, url, []byte("x"), nil); status != http.StatusCreated {
			t.Fatalf("PUT %c: status %d", c, status)
		}
	}
	if versions := cache.versions(); len(versions) != 16 {
		t.Errorf("entries in %d versions, want 16: %v", len(versions), versions)
	}
}

func TestRejectsMalformedObjects(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, path := range []string{
		"/v1/blobs/md5/" + hash('a') + "/1",
		"/v1/blobs/blake3/" + strings.ToUpper(hash('a')) + "/1",
		"/v1/blobs/blake3/" + hash('a')[:63] + "/1",
		"/v1/blobs/blake3/" + hash('a') + "/01",
		"/v1/blobs/blake3/" + hash('a') + "/-1",
		"/v1/trees/blake3/" + hash('a') + "/1",
		"/v2/blobs/blake3/" + hash('a') + "/1",
	} {
		if status, _, _ := do(t, http.MethodGet, srv.URL+path, nil, nil); status != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, status)
		}
	}
	// A blob whose body does not have its digest's size.
	url := fmt.Sprintf("%s/v1/blobs/blake3/%s/5", srv.URL, hash('d'))
	if status, _, _ := do(t, http.MethodPut, url, []byte("four"), nil); status != http.StatusBadRequest {
		t.Errorf("short blob: status %d, want 400", status)
	}
	if status, _, _ := do(t, http.MethodDelete, url, nil, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status %d, want 405", status)
	}
}

func TestUpdatesManifestsConditionally(t *testing.T) {
	srv, _, server := newTestServer(t)
	url := fmt.Sprintf("%s/v1/action-manifests/blake3/%s/7", srv.URL, hash('e'))
	if status, _, _ := do(t, http.MethodGet, url, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET before PUT: status %d, want 404", status)
	}
	status, header, _ := do(t, http.MethodPut, url, []byte(`{"n":1}`), map[string]string{"If-None-Match": "*"})
	first := header.Get("ETag")
	if status != http.StatusCreated || !strings.HasPrefix(first, `"`) {
		t.Fatalf("first PUT: status %d, ETag %q", status, first)
	}
	// Someone else published first.
	if status, _, _ := do(t, http.MethodPut, url, []byte(`{"n":2}`), map[string]string{"If-None-Match": "*"}); status != http.StatusPreconditionFailed {
		t.Fatalf("second create: status %d, want 412", status)
	}
	status, header, body := do(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK || header.Get("ETag") != first || string(body) != `{"n":1}` {
		t.Fatalf("GET: status %d, ETag %q (want %q), body %s", status, header.Get("ETag"), first, body)
	}
	if status, _, _ := do(t, http.MethodPut, url, []byte(`{"n":3}`), map[string]string{"If-Match": `"stale"`}); status != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status %d, want 412", status)
	}
	status, header, _ = do(t, http.MethodPut, url, []byte(`{"n":4}`), map[string]string{"If-Match": first})
	second := header.Get("ETag")
	if status != http.StatusCreated || second == first || second == "" {
		t.Fatalf("If-Match update: status %d, ETag %q", status, second)
	}
	status, header, body = do(t, http.MethodGet, url, nil, nil)
	if status != http.StatusOK || header.Get("ETag") != second || string(body) != `{"n":4}` {
		t.Fatalf("GET after update: status %d, ETag %q, body %s", status, header.Get("ETag"), body)
	}
	if status, _, _ := do(t, http.MethodPut, url, []byte(`{"n":5}`), map[string]string{"If-Match": first}); status != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on the replaced revision: status %d, want 412", status)
	}
	if stats := server.Stats(); stats.Manifests != 2 || stats.Conflicted != 3 {
		t.Errorf("stats %+v", stats)
	}
}

func TestAnswersARefusedWriteWith403(t *testing.T) {
	srv, cache, server := newTestServer(t)
	cache.setReadOnly(true)
	url := fmt.Sprintf("%s/v1/blobs/blake3/%s/1", srv.URL, hash('f'))
	if status, _, _ := do(t, http.MethodPut, url, []byte("x"), nil); status != http.StatusForbidden {
		t.Fatalf("PUT with a read-only token: status %d, want 403", status)
	}
	if stats := server.Stats(); stats.Refused != 1 || stats.Errors != 0 {
		t.Errorf("stats %+v", stats)
	}
}

func TestStopsOnlyWhenTheActionAsksWithTheClientToken(t *testing.T) {
	srv, _, server := newTestServer(t)
	if status, _, _ := do(t, http.MethodPost, srv.URL+ShutdownPath, nil, map[string]string{"Authorization": "Bearer wrong"}); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d, want 401", status)
	}
	select {
	case <-server.Stopped():
		t.Fatal("stopped for a request without the client token")
	default:
	}
	if status, _, _ := do(t, http.MethodPost, srv.URL+ShutdownPath, nil, nil); status != http.StatusAccepted {
		t.Fatalf("status %d, want 202", status)
	}
	select {
	case <-server.Stopped():
	default:
		t.Fatal("not stopped")
	}
	// Asking twice is harmless.
	if status, _, _ := do(t, http.MethodPost, srv.URL+ShutdownPath, nil, nil); status != http.StatusAccepted {
		t.Fatalf("second request: status %d, want 202", status)
	}
}

func TestServesStats(t *testing.T) {
	srv, _, _ := newTestServer(t)
	status, _, body := do(t, http.MethodGet, srv.URL+StatsPath, nil, nil)
	var stats Stats
	if status != http.StatusOK || json.Unmarshal(body, &stats) != nil {
		t.Fatalf("status %d, body %s", status, body)
	}
}
