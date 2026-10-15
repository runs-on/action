package mbx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runs-on/action/internal/magiccache"
)

// mbx's remote cache protocol, version 1. The helper serves its baseline
// (action results and blobs) and its conditional action manifests, and
// advertises no other extension, so mbx sends single objects without
// compression:
// https://github.com/jdx/mr-boxington/blob/main/docs/protocol-compatibility.md
const (
	protocolHeader    = "mbx-cache-protocol"
	namespaceHeader   = "mbx-cache-namespace"
	actionResultMedia = "application/vnd.mbx.cache-action-result.v1+json"
	manifestMedia     = "application/vnd.mbx.cache-task-action-manifest.v1+json"
	blobMedia         = "application/octet-stream"
)

// entryFormat versions how objects map to cache entries: a change starts from
// an empty cache.
const entryFormat = "runs-on-mbx-v1"

// What the helper accepts, as large as what mbx sends: JSON records up to 16
// MiB, blobs up to 5 GiB, which is also the most one S3 upload takes.
const (
	maxRecordBytes = 16 << 20
	maxBlobBytes   = 5 << 30
)

// The action's post step reads the helper's counters and stops it through
// these paths, which are not part of mbx's protocol. They need the client
// token like every other request, so the post step only ever stops this job's
// helper.
const (
	StatsPath    = "/runs-on/stats"
	ShutdownPath = "/runs-on/shutdown"
)

// Stats counts what the helper served.
type Stats struct {
	Hits       int64 `json:"hits"`
	Misses     int64 `json:"misses"`
	Uploads    int64 `json:"uploads"`
	Existing   int64 `json:"existing"`
	Errors     int64 `json:"errors"`
	BytesDown  int64 `json:"bytes_down"`
	BytesUp    int64 `json:"bytes_up"`
	Refused    int64 `json:"refused"`
	Manifests  int64 `json:"manifests"`
	Conflicted int64 `json:"conflicted"`
}

// Server serves mbx's remote cache protocol from Magic Cache. Every object is
// a GitHub Actions cache entry, so the job's cache scopes apply: it reads its
// own ref, then its base and default branches, and writes only its own ref.
type Server struct {
	cache       *magiccache.CacheClient
	clientToken string
	log         *slog.Logger

	// Manifest updates are a read and a write; one at a time per manifest
	// within the job.
	manifestMu    sync.Mutex
	manifestLocks map[string]*sync.Mutex

	stopOnce sync.Once
	stopped  chan struct{}

	hits, misses, uploads, existing, errs, bytesDown, bytesUp, refused, manifests, conflicted atomic.Int64
}

// NewServer returns the helper's handler. Requests must carry clientToken as
// a bearer token.
func NewServer(cache *magiccache.CacheClient, clientToken string, log *slog.Logger) *Server {
	return &Server{cache: cache, clientToken: clientToken, log: log, manifestLocks: map[string]*sync.Mutex{}, stopped: make(chan struct{})}
}

// Stopped is closed once the post step asked the helper to stop.
func (s *Server) Stopped() <-chan struct{} {
	return s.stopped
}

// Stats returns the counters.
func (s *Server) Stats() Stats {
	return Stats{
		Hits: s.hits.Load(), Misses: s.misses.Load(), Uploads: s.uploads.Load(), Existing: s.existing.Load(),
		Errors: s.errs.Load(), BytesDown: s.bytesDown.Load(), BytesUp: s.bytesUp.Load(), Refused: s.refused.Load(),
		Manifests: s.manifests.Load(), Conflicted: s.conflicted.Load(),
	}
}

type kind string

const (
	actionResult kind = "action-results"
	blob         kind = "blobs"
	manifest     kind = "action-manifests"
)

// object is one of mbx's addressed resources: a kind and a digest.
type object struct {
	kind      kind
	algorithm string
	hash      string
	size      uint64
	namespace string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == StatsPath && r.Method == http.MethodGet {
		writeJSON(w, s.Stats())
		return
	}
	if r.URL.Path == ShutdownPath && r.Method == http.MethodPost {
		w.WriteHeader(http.StatusAccepted)
		s.stopOnce.Do(func() { close(s.stopped) })
		return
	}
	if v := r.Header.Get(protocolHeader); v != "" && v != "1" {
		http.Error(w, "unsupported mbx-cache-protocol "+v, http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/v1/capabilities" && r.Method == http.MethodGet {
		writeJSON(w, map[string]any{
			"protocol":          map[string]int{"major": 1, "minor": 0},
			"digest_algorithms": []string{"blake3", "sha256"},
			"features":          map[string]bool{"action_manifests": true},
		})
		return
	}
	obj, ok := parseObject(r.URL.Path)
	if !ok {
		// Batches, packs and promises are optional: a 404 tells mbx to send
		// single objects instead.
		http.NotFound(w, r)
		return
	}
	obj.namespace = r.Header.Get(namespaceHeader)
	if obj.namespace == "" {
		http.Error(w, "missing "+namespaceHeader+" header", http.StatusBadRequest)
		return
	}
	switch {
	case obj.kind == manifest && r.Method == http.MethodGet:
		s.getManifest(w, r, obj)
	case obj.kind == manifest && r.Method == http.MethodPut:
		s.putManifest(w, r, obj)
	case r.Method == http.MethodGet:
		s.get(w, r, obj)
	case r.Method == http.MethodPut:
		s.put(w, r, obj)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.clientToken)) == 1
}

// parseObject reads /v1/<kind>/<algorithm>/<hash>/<size>.
func parseObject(path string) (object, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if !strings.HasPrefix(path, "/v1/") || len(parts) != 4 {
		return object{}, false
	}
	obj := object{kind: kind(parts[0]), algorithm: parts[1], hash: parts[2]}
	switch obj.kind {
	case actionResult, blob, manifest:
	default:
		return object{}, false
	}
	if obj.algorithm != "blake3" && obj.algorithm != "sha256" {
		return object{}, false
	}
	if len(obj.hash) != 64 || strings.Trim(obj.hash, "0123456789abcdef") != "" {
		return object{}, false
	}
	size, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || strconv.FormatUint(size, 10) != parts[3] {
		return object{}, false
	}
	obj.size = size
	return obj, true
}

// key names the object's cache entry. The namespace is hashed: it is any
// string mbx was given, and keys must stay short and free of commas.
func (o object) key() string {
	ns := sha256.Sum256([]byte(o.namespace))
	return fmt.Sprintf("mbx-%s-%s-%s-%s-%d", hex.EncodeToString(ns[:8]), o.kind, o.algorithm, o.hash, o.size)
}

// version spreads entries over 16 cache versions by the hash's first hex
// digit. Each version is one directory in Magic Cache, whose lookup index
// covers a directory only while it holds fewer than 50,000 entries.
func (o object) version() string {
	return entryFormat + "-" + string(o.kind) + "-" + o.hash[:1]
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, obj object) {
	body, _, ok, err := s.cache.Open(r.Context(), obj.version(), obj.key())
	if err != nil {
		s.fail(w, "read", obj, err)
		return
	}
	if !ok {
		s.misses.Add(1)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = body.Close() }()
	s.hits.Add(1)
	if obj.kind == blob {
		w.Header().Set("Content-Type", blobMedia)
	} else {
		w.Header().Set("Content-Type", actionResultMedia)
	}
	n, err := io.Copy(w, body)
	s.bytesDown.Add(n)
	if err != nil {
		// The status is sent: mbx sees a short body and verifies the digest.
		s.errs.Add(1)
		s.log.Warn("download broke off", "kind", obj.kind, "hash", obj.hash, "error", err)
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, obj object) {
	size, ok := s.contentLength(w, r, obj)
	if !ok {
		return
	}
	err := s.cache.Upload(r.Context(), obj.version(), obj.key(), r.Body, size)
	switch {
	case errors.Is(err, magiccache.ErrEntryExists):
		// Objects are content-addressed: what is stored is what mbx sent.
		s.existing.Add(1)
		w.WriteHeader(http.StatusPreconditionFailed)
	case err != nil:
		s.fail(w, "write", obj, err)
	default:
		s.uploads.Add(1)
		s.bytesUp.Add(size)
		w.WriteHeader(http.StatusCreated)
	}
}

// contentLength checks the upload's declared length: mbx sends it, and the
// cache entry is created with one PUT of that many bytes.
func (s *Server) contentLength(w http.ResponseWriter, r *http.Request, obj object) (int64, bool) {
	size := r.ContentLength
	limit := int64(maxRecordBytes)
	if obj.kind == blob {
		limit = maxBlobBytes
	}
	switch {
	case size < 0:
		http.Error(w, "Content-Length required", http.StatusLengthRequired)
		return 0, false
	case size > limit:
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return 0, false
	case obj.kind == blob && uint64(size) != obj.size:
		http.Error(w, "body length does not match the digest's size", http.StatusBadRequest)
		return 0, false
	}
	return size, true
}

// Manifests change: each revision is a new cache entry under the manifest's
// prefix, and the newest one is the manifest. Its strong ETag is the
// revision, which a conditional update must name.
func (o object) manifestPrefix() string {
	return o.key() + "-"
}

func (s *Server) currentRevision(ctx context.Context, obj object) (string, bool, error) {
	prefix := obj.manifestPrefix()
	matched, ok, err := s.cache.Lookup(ctx, obj.version(), prefix, prefix)
	if err != nil || !ok {
		return "", false, err
	}
	revision, ok := strings.CutPrefix(matched, prefix)
	return revision, ok && revision != "", nil
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request, obj object) {
	prefix := obj.manifestPrefix()
	body, matched, ok, err := s.cache.Open(r.Context(), obj.version(), prefix, prefix)
	if err != nil {
		s.fail(w, "read", obj, err)
		return
	}
	if !ok {
		s.misses.Add(1)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = body.Close() }()
	revision, ok := strings.CutPrefix(matched, prefix)
	if !ok || revision == "" {
		s.misses.Add(1)
		http.NotFound(w, r)
		return
	}
	s.hits.Add(1)
	w.Header().Set("Content-Type", manifestMedia)
	w.Header().Set("ETag", `"`+revision+`"`)
	n, _ := io.Copy(w, body)
	s.bytesDown.Add(n)
}

func (s *Server) putManifest(w http.ResponseWriter, r *http.Request, obj object) {
	size, ok := s.contentLength(w, r, obj)
	if !ok {
		return
	}
	unlock := s.lockManifest(obj.manifestPrefix())
	defer unlock()
	current, exists, err := s.currentRevision(r.Context(), obj)
	if err != nil {
		s.fail(w, "read", obj, err)
		return
	}
	ifMatch, ifNoneMatch := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if (ifNoneMatch == "*" && exists) || (ifMatch != "" && (!exists || ifMatch != `"`+current+`"`)) {
		// Another job published since mbx read the manifest: mbx merges its
		// predictions into the newer one and tries again.
		s.conflicted.Add(1)
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	revision := newRevision()
	err = s.cache.Upload(r.Context(), obj.version(), obj.manifestPrefix()+revision, r.Body, size)
	switch {
	case errors.Is(err, magiccache.ErrEntryExists):
		s.conflicted.Add(1)
		w.WriteHeader(http.StatusPreconditionFailed)
	case err != nil:
		s.fail(w, "write", obj, err)
	default:
		s.manifests.Add(1)
		s.bytesUp.Add(size)
		w.Header().Set("ETag", `"`+revision+`"`)
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) lockManifest(prefix string) func() {
	s.manifestMu.Lock()
	mu, ok := s.manifestLocks[prefix]
	if !ok {
		mu = &sync.Mutex{}
		s.manifestLocks[prefix] = mu
	}
	s.manifestMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// newRevision names a manifest revision: the time, so revisions sort, and
// random bytes, so two jobs never pick the same one.
func newRevision() string {
	var random [6]byte
	_, _ = rand.Read(random[:])
	return fmt.Sprintf("%016x%s", time.Now().UnixNano(), hex.EncodeToString(random[:]))
}

// fail answers a cache error. mbx retries a 5xx, and builds locally once it
// gives up; a token that may not write gets a 403, which mbx does not retry.
func (s *Server) fail(w http.ResponseWriter, op string, obj object, err error) {
	if magiccache.IsForbidden(err) {
		s.refused.Add(1)
		s.log.Warn("Magic Cache refused the job's token", "op", op, "kind", obj.kind, "error", err)
		http.Error(w, "Magic Cache refused this job's token", http.StatusForbidden)
		return
	}
	s.errs.Add(1)
	s.log.Warn("Magic Cache request failed", "op", op, "kind", obj.kind, "hash", obj.hash, "error", err)
	http.Error(w, "Magic Cache request failed", http.StatusBadGateway)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
