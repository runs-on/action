package gocacheprog

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"golang.org/x/sync/singleflight"
)

// Remote entries hold one action: a magic header, the time the entry was
// first stored (8 bytes, Unix nanoseconds), the 32-byte output ID, then the
// output. Go action and output IDs are SHA-256 hashes of every input
// (sources, flags, toolchain, GOOS/GOARCH), so a key never needs a platform.
// Test records have their own magic, so a job that does not share test
// results rejects them from the header.
var (
	buildMagic = []byte("RGB1")
	testMagic  = []byte("RGT1")
)

const (
	magicSize    = 4
	timeSize     = 8
	outputIDSize = 32
	actionIDSize = 32
	headerSize   = magicSize + timeSize + outputIDSize
)

// The go command caches a test result as up to three records
// (cmd/go/internal/test): what the test read (files, environment variables),
// which starts with testLogMagic; what it printed, whose last line starts
// with testResultPrefix (both formats unchanged since Go 1.10); and, with
// -coverprofile, its coverage profile, whose first line is coverProfilePrefix
// and a mode. Keeping them on local disk only makes every job run its tests:
// a result is reused within a job, as on a laptop, but a test that passed in
// another job is not skipped, and what it printed or covered does not leave
// the job. Compiled test code is still shared.
var (
	testLogMagic       = []byte("# test log\n")
	testResultPrefix   = []byte("ok  \t")
	coverProfilePrefix = []byte("mode: ")
)

func isTestRecord(body []byte) bool {
	if bytes.HasPrefix(body, testLogMagic) {
		return true
	}
	if firstLine, _, ok := bytes.Cut(body, []byte("\n")); ok && bytes.HasPrefix(firstLine, coverProfilePrefix) {
		switch string(firstLine[len(coverProfilePrefix):]) {
		case "set", "count", "atomic":
			return true
		}
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return false
	}
	lastLine := body[bytes.LastIndexByte(body[:len(body)-1], '\n')+1:]
	return bytes.HasPrefix(lastLine, testResultPrefix)
}

var errCorruptEntry = errors.New("remote entry does not match its output ID")

// Options configures a Cache.
type Options struct {
	Dir        string
	ResultsURL string
	Token      string
	// Namespace prefixes remote keys and versions, so a format change starts
	// from an empty cache.
	Namespace string
	// ShareTestResults also shares test records. Without it they stay on
	// local disk, and the ones other jobs shared are ignored.
	ShareTestResults bool
	// Concurrency bounds background uploads and prefetches.
	Concurrency int
	// CloseTimeout bounds what the go command waits for when it exits: the
	// pending uploads.
	CloseTimeout time.Duration
}

// Cache is the Handler: local disk first, then the remote cache.
type Cache struct {
	opts   Options
	disk   *diskStore
	remote *magiccache.CacheClient
	// remoteDown stops remote lookups and uploads for this go command after a
	// failure: a cache that times out or rejects the job's token would
	// otherwise cost every lookup of the build a doomed request.
	remoteDown atomic.Bool
	// ctx is cancelled once the go command stops waiting for uploads.
	ctx    context.Context
	cancel context.CancelFunc

	// fetches makes concurrent lookups of the same entry share one download.
	fetches singleflight.Group

	// used holds the entries this process already recorded in the journal of
	// what the job used.
	usedMu  sync.Mutex
	used    map[[actionIDSize]byte]struct{}
	journal *os.File

	uploads  chan upload
	uploadWG sync.WaitGroup
}

// upload names an output already on local disk, so a queue of pending
// uploads holds no output in memory.
type upload struct {
	actionID []byte
	outputID []byte
	stored   time.Time
	test     bool
}

// uploadQueueSize bounds the pending uploads. Uploads are best effort: when
// the cache falls this far behind, more outputs are only kept locally.
const uploadQueueSize = 65536

// New opens the cache. Without a results URL or token it only uses local
// disk.
func New(opts Options) (*Cache, error) {
	disk, err := newDiskStore(opts.Dir)
	if err != nil {
		return nil, err
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 64
	}
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = 2 * time.Minute
	}
	c := &Cache{opts: opts, disk: disk, used: map[[actionIDSize]byte]struct{}{}}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if opts.ResultsURL != "" && opts.Token != "" {
		c.remote = magiccache.NewCacheClient(opts.ResultsURL, opts.Token)
		c.uploads = make(chan upload, uploadQueueSize)
		for range opts.Concurrency {
			go c.uploadLoop()
		}
		// Every go command of the job appends to the same journal. Without
		// it the build still works: the job's next run prefetches less.
		c.journal, _ = os.OpenFile(filepath.Join(opts.Dir, usedFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	return c, nil
}

func (c *Cache) key(actionID []byte) string {
	return c.opts.Namespace + "-" + hex.EncodeToString(actionID)
}

// version shards entries across 16 cache versions by the first hex digit of
// their action ID. Each version is one directory in Magic Cache, whose lookup
// index covers a directory only while it holds fewer than 50,000 entries, and
// only once 8 lookups asked for it: 16 directories hold 800,000 entries and
// cost a cold build 128 listed lookups.
func (c *Cache) version(actionID []byte) string {
	return c.opts.Namespace + "-" + hex.EncodeToString(actionID[:1])[:1]
}

func (c *Cache) remoteUp() bool {
	return c.remote != nil && !c.remoteDown.Load()
}

// markUsed records an entry in the journal of what the job used, which its
// post step saves for the job's next run to prefetch.
func (c *Cache) markUsed(actionID []byte) {
	if len(actionID) != actionIDSize || c.journal == nil {
		return
	}
	c.usedMu.Lock()
	defer c.usedMu.Unlock()
	id := [actionIDSize]byte(actionID)
	if _, ok := c.used[id]; !ok {
		c.used[id] = struct{}{}
		_, _ = c.journal.Write(id[:])
	}
}

// Get implements Handler.
func (c *Cache) Get(actionID []byte) (Entry, bool, error) {
	entry, local, ok := c.disk.get(actionID)
	if !ok && len(actionID) == actionIDSize && c.fetch(actionID) {
		entry, local, ok = c.disk.get(actionID)
	}
	// A record kept on local disk is not in the remote cache: the job's next
	// run would have nothing to prefetch for it.
	if ok && !local {
		c.markUsed(actionID)
	}
	return entry, ok, nil
}

// fetch downloads an entry to local disk, once even when it is asked for
// several times at once. A cache that fails or holds a corrupt entry must not
// fail the build: it reports a miss, and a failing cache is not asked again
// by this process.
func (c *Cache) fetch(actionID []byte) bool {
	found, _, _ := c.fetches.Do(string(actionID), func() (any, error) {
		if _, _, ok := c.disk.get(actionID); ok {
			return true, nil
		}
		if !c.remoteUp() {
			return false, nil
		}
		body, _, ok, err := c.remote.Open(c.ctx, c.version(actionID), c.key(actionID))
		if err != nil {
			c.remoteDown.Store(true)
			return false, nil
		}
		if !ok {
			return false, nil
		}
		defer func() { _ = body.Close() }()
		// The output goes from the response to local disk, so concurrent
		// downloads hold no output in memory.
		header := make([]byte, headerSize)
		if _, err := io.ReadFull(body, header); err != nil {
			// Every entry starts with a header: the transfer broke off.
			c.remoteDown.Store(true)
			return false, nil
		}
		magic, rest := header[:magicSize], header[magicSize:]
		if !bytes.Equal(magic, buildMagic) && !(c.opts.ShareTestResults && bytes.Equal(magic, testMagic)) {
			return false, nil
		}
		// The entry keeps the time it was first stored: the go command
		// expires test results older than its last go clean -testcache.
		stored := time.Unix(0, int64(binary.BigEndian.Uint64(rest[:timeSize])))
		err = c.disk.putStream(actionID, rest[timeSize:], body, stored)
		if err != nil && !errors.Is(err, errCorruptEntry) {
			c.remoteDown.Store(true)
		}
		return err == nil, nil
	})
	return found.(bool)
}

// Put implements Handler. The upload runs in the background; Close waits for
// it.
func (c *Cache) Put(actionID, outputID, body []byte) (string, error) {
	if len(outputID) != outputIDSize {
		return "", fmt.Errorf("output ID is %d bytes, want %d", len(outputID), outputIDSize)
	}
	stored := time.Now()
	test := isTestRecord(body)
	local := test && !c.opts.ShareTestResults
	path, err := c.disk.put(actionID, outputID, body, stored, local)
	if err != nil || local {
		return path, err
	}
	c.markUsed(actionID)
	if c.remoteUp() && len(actionID) == actionIDSize {
		c.uploadWG.Add(1)
		select {
		case c.uploads <- upload{actionID: actionID, outputID: outputID, stored: stored, test: test}:
		default:
			c.uploadWG.Done()
		}
	}
	return path, nil
}

// uploadLoop uploads entries in the background. An entry another job already
// saved costs this one entry; a failing cache stops this go command's
// uploads.
func (c *Cache) uploadLoop() {
	for u := range c.uploads {
		if c.remoteUp() {
			if err := c.uploadEntry(u); err != nil && !errors.Is(err, magiccache.ErrEntryExists) {
				c.remoteDown.Store(true)
			}
		}
		c.uploadWG.Done()
	}
}

// uploadEntry streams the output from local disk, so pending uploads hold no
// output in memory whatever its size.
func (c *Cache) uploadEntry(u upload) error {
	f, err := os.Open(c.disk.outputPath(u.outputID))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	magic := buildMagic
	if u.test {
		magic = testMagic
	}
	header := binary.BigEndian.AppendUint64(bytes.Clone(magic), uint64(u.stored.UnixNano()))
	header = append(header, u.outputID...)
	body := io.MultiReader(bytes.NewReader(header), f)
	return c.remote.Upload(c.ctx, c.version(u.actionID), c.key(u.actionID), body, int64(len(header))+info.Size())
}

// Close implements Handler: it waits for pending uploads, within CloseTimeout.
func (c *Cache) Close() error {
	defer c.cancel()
	if c.remote == nil {
		return nil
	}
	if c.journal != nil {
		_ = c.journal.Close()
	}
	done := make(chan struct{})
	go func() {
		c.uploadWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(c.opts.CloseTimeout):
	}
	return nil
}
