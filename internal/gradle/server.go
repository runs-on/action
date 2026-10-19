package gradle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"golang.org/x/sync/singleflight"
)

// The server's routes: Gradle's HTTP build cache protocol under cachePath,
// and what the action asks of it.
const (
	cachePath  = "/cache/"
	healthPath = "/_runs-on/health"
	stopPath   = "/_runs-on/stop"
)

// healthBody tells the server from whatever else could answer on its port.
const healthBody = "runs-on gradle build cache"

// namespace versions the remote entries: a format change starts from an
// empty cache.
const namespace = "runs-on-gradle-v1"

// maxEntrySize keeps outsized entries out of the cache. Gradle takes 413 as
// an entry the cache does not store, not as an error.
const maxEntrySize = 1 << 30

// uploadQueueSize bounds the pending uploads. Uploads are best effort: when
// Magic Cache falls this far behind, more entries only stay on the runner.
const uploadQueueSize = 65536

// remoteFailureLimit is how many Magic Cache calls in a row may fail before
// the server stops asking it: a cache that is down would otherwise cost every
// task of the build a doomed request.
const remoteFailureLimit = 3

// Stats is what the server did for the job's builds.
type Stats struct {
	// Gradle asked for LocalHits+RemoteHits+Misses entries. LocalHits were
	// stored earlier in the job, RemoteHits came from Magic Cache.
	LocalHits  int64 `json:"localHits"`
	RemoteHits int64 `json:"remoteHits"`
	Misses     int64 `json:"misses"`
	// RemoteWait is how long Gradle waited on Magic Cache for its loads.
	RemoteWait      time.Duration `json:"remoteWait"`
	DownloadedBytes int64         `json:"downloadedBytes"`
	// Gradle stored Stores entries. Uploaded of them reached Magic Cache,
	// AlreadyStored were there already, and Pending were still waiting when
	// the server stopped.
	Stores        int64 `json:"stores"`
	Uploaded      int64 `json:"uploaded"`
	AlreadyStored int64 `json:"alreadyStored"`
	UploadedBytes int64 `json:"uploadedBytes"`
	Pending       int64 `json:"pending"`
	// RemoteErrors counts Magic Cache calls that failed.
	RemoteErrors int64 `json:"remoteErrors"`
}

type counters struct {
	localHits, remoteHits, misses, remoteWait, downloadedBytes     atomic.Int64
	stores, uploaded, alreadyStored, uploadedBytes, queued, errors atomic.Int64
}

// Server serves Gradle's HTTP build cache from local disk and Magic Cache.
// Gradle's stores land on local disk, so Gradle never waits on Magic Cache to
// store an entry: the server uploads them in the background, and the action's
// post step waits for the uploads before the job ends.
type Server struct {
	dir    string
	remote *magiccache.Client
	logger *log.Logger
	// failures counts consecutive failed Magic Cache calls.
	failures atomic.Int32
	// fetches makes concurrent loads of the same entry share one download.
	fetches  singleflight.Group
	uploads  chan string
	uploadWG sync.WaitGroup
	stats    counters
	// ctx is cancelled once the server stops waiting for uploads.
	ctx    context.Context
	cancel context.CancelFunc
	// stopped is closed once the action asked the server to stop.
	stopped  chan struct{}
	stopOnce sync.Once
}

// NewServer opens the server's local entries in dir. Without a remote
// client, it only keeps entries on local disk.
func NewServer(dir string, remote *magiccache.Client, logger *log.Logger) (*Server, error) {
	for _, sub := range []string{"entries", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	s := &Server{dir: dir, remote: remote, logger: logger, stopped: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if remote != nil {
		s.uploads = make(chan string, uploadQueueSize)
		for range 32 {
			go s.uploadLoop()
		}
	}
	return s, nil
}

// Stopped is closed once the action asked the server to stop.
func (s *Server) Stopped() <-chan struct{} {
	return s.stopped
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, cachePath):
		key := strings.TrimPrefix(r.URL.Path, cachePath)
		if !validKey(key) {
			http.Error(w, "invalid build cache key", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.load(w, key)
		case http.MethodPut:
			s.store(w, r, key)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case r.URL.Path == healthPath && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, healthBody)
	case r.URL.Path == stopPath && r.Method == http.MethodPost:
		wait, err := time.ParseDuration(r.URL.Query().Get("wait"))
		if err != nil {
			wait = 0
		}
		stats := s.Stop(wait)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	default:
		http.NotFound(w, r)
	}
}

// validKey accepts Gradle's build cache keys, lowercase hex hashes, so a key
// is always a safe file name and a valid Magic Cache key.
func validKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) entryPath(key string) string {
	return filepath.Join(s.dir, "entries", key)
}

// remoteKey and remoteVersion name an entry in Magic Cache. Versions shard
// entries by the first hex digit of their key: each version is one directory
// in Magic Cache, whose lookups list it.
func remoteKey(key string) string {
	return namespace + "-" + key
}

func remoteVersion(key string) string {
	return namespace + "-" + key[:1]
}

func (s *Server) remoteUp() bool {
	return s.remote != nil && s.failures.Load() < remoteFailureLimit
}

func (s *Server) remoteFailed(what string, err error) {
	s.stats.errors.Add(1)
	if s.failures.Add(1) == remoteFailureLimit {
		s.logger.Printf("%s: %v; Magic Cache failed %d times in a row, so the server stops using it", what, err, remoteFailureLimit)
		return
	}
	s.logger.Printf("%s: %v", what, err)
}

func (s *Server) load(w http.ResponseWriter, key string) {
	if serveFile(w, s.entryPath(key)) {
		s.stats.localHits.Add(1)
		return
	}
	start := time.Now()
	found := s.fetch(key)
	s.stats.remoteWait.Add(int64(time.Since(start)))
	if found && serveFile(w, s.entryPath(key)) {
		s.stats.remoteHits.Add(1)
		return
	}
	s.stats.misses.Add(1)
	w.WriteHeader(http.StatusNotFound)
}

func serveFile(w http.ResponseWriter, path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, f)
	return true
}

// fetch downloads an entry from Magic Cache to local disk, once even when
// Gradle asks for it several times at once. A download that fails is a miss:
// Gradle then runs the task.
func (s *Server) fetch(key string) bool {
	found, _, _ := s.fetches.Do(key, func() (any, error) {
		if _, err := os.Stat(s.entryPath(key)); err == nil {
			return true, nil
		}
		if !s.remoteUp() {
			return false, nil
		}
		body, ok, err := s.remote.Open(s.ctx, remoteVersion(key), remoteKey(key))
		if err != nil {
			s.remoteFailed("look up "+key, err)
			return false, nil
		}
		s.failures.Store(0)
		if !ok {
			return false, nil
		}
		defer func() { _ = body.Close() }()
		size, err := s.writeEntry(key, body)
		if err != nil {
			s.remoteFailed("download "+key, err)
			return false, nil
		}
		s.stats.downloadedBytes.Add(size)
		return true, nil
	})
	return found.(bool)
}

// store keeps the entry on local disk, answers Gradle, and queues the upload.
func (s *Server) store(w http.ResponseWriter, r *http.Request, key string) {
	if r.ContentLength > maxEntrySize {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	size, err := s.writeEntry(key, io.LimitReader(r.Body, maxEntrySize+1))
	if err != nil {
		s.logger.Printf("store %s: %v", key, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if size > maxEntrySize {
		_ = os.Remove(s.entryPath(key))
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	s.stats.stores.Add(1)
	if s.remoteUp() {
		s.uploadWG.Add(1)
		s.stats.queued.Add(1)
		select {
		case s.uploads <- key:
		default:
			s.stats.queued.Add(-1)
			s.uploadWG.Done()
		}
	}
	w.WriteHeader(http.StatusCreated)
}

// writeEntry writes an entry through a temporary file, so a load never reads
// a partial one, and returns its size.
func (s *Server) writeEntry(key string, body io.Reader) (int64, error) {
	f, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), key+"-*")
	if err != nil {
		return 0, err
	}
	size, err := io.Copy(f, body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.entryPath(key))
		// Windows refuses to replace a file that is open, being uploaded or
		// served. What is there is then the same entry, stored earlier.
		if _, statErr := os.Stat(s.entryPath(key)); err != nil && statErr == nil {
			err = nil
		}
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return 0, err
	}
	_ = os.Remove(f.Name())
	return size, nil
}

// uploadLoop uploads stored entries to Magic Cache.
func (s *Server) uploadLoop() {
	for key := range s.uploads {
		if s.remoteUp() {
			s.upload(key)
		}
		s.stats.queued.Add(-1)
		s.uploadWG.Done()
	}
}

func (s *Server) upload(key string) {
	f, err := os.Open(s.entryPath(key))
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return
	}
	err = s.remote.Upload(s.ctx, remoteVersion(key), remoteKey(key), f, info.Size())
	switch {
	case err == nil:
		s.failures.Store(0)
		s.stats.uploaded.Add(1)
		s.stats.uploadedBytes.Add(info.Size())
	case errors.Is(err, magiccache.ErrEntryExists):
		s.failures.Store(0)
		s.stats.alreadyStored.Add(1)
	case s.ctx.Err() != nil:
		// The server stopped waiting for uploads.
	default:
		s.remoteFailed("upload "+key, err)
	}
}

// Stop waits up to wait for the pending uploads, cancels the ones left, and
// returns what the server did. The server exits once it answered.
func (s *Server) Stop(wait time.Duration) Stats {
	done := make(chan struct{})
	go func() {
		s.uploadWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
	}
	stats := s.Stats()
	s.cancel()
	s.stopOnce.Do(func() { close(s.stopped) })
	return stats
}

// Stats returns what the server did so far.
func (s *Server) Stats() Stats {
	return Stats{
		LocalHits:       s.stats.localHits.Load(),
		RemoteHits:      s.stats.remoteHits.Load(),
		Misses:          s.stats.misses.Load(),
		RemoteWait:      time.Duration(s.stats.remoteWait.Load()),
		DownloadedBytes: s.stats.downloadedBytes.Load(),
		Stores:          s.stats.stores.Load(),
		Uploaded:        s.stats.uploaded.Load(),
		AlreadyStored:   s.stats.alreadyStored.Load(),
		UploadedBytes:   s.stats.uploadedBytes.Load(),
		Pending:         s.stats.queued.Load(),
		RemoteErrors:    s.stats.errors.Load(),
	}
}
