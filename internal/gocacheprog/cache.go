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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

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

// The go command caches a test result as two records (cmd/go/internal/test,
// both formats unchanged since Go 1.10): what the test read (files,
// environment variables), which starts with testLogMagic, and what it
// printed, whose last line starts with testResultPrefix. Keeping both on
// local disk only makes every job run its tests: a result is reused within a
// job, as on a laptop, but a test that passed in another job is not skipped,
// and what it printed does not leave the job. Compiled test code is still
// shared.
var (
	testLogMagic     = []byte("# test log\n")
	testResultPrefix = []byte("ok  \t")
)

func isTestRecord(body []byte) bool {
	if bytes.HasPrefix(body, testLogMagic) {
		return true
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
	// ManifestKey names the list of entries this go command used, which the
	// next run of the same command prefetches. Empty disables prefetching.
	ManifestKey string
	// ShareTestResults also shares test records. Without it they stay on
	// local disk, and the ones other jobs shared are ignored.
	ShareTestResults bool
	// Concurrency bounds background uploads and prefetches.
	Concurrency int
	// CloseTimeout bounds what the go command waits for when it exits: the
	// pending uploads and the manifest.
	CloseTimeout time.Duration
}

// Cache is the Handler: local disk first, then the remote cache.
type Cache struct {
	opts   Options
	disk   *diskStore
	remote *ghaClient
	// remoteDown stops remote lookups and uploads for this go command after a
	// failure: a cache that times out would otherwise add its timeout to
	// every lookup of the build.
	remoteDown atomic.Bool
	// ctx is cancelled once the go command stops waiting for uploads.
	ctx    context.Context
	cancel context.CancelFunc

	// fetches makes a prefetch and the go command's own lookup of the same
	// entry share one download.
	fetches singleflight.Group

	usedMu sync.Mutex
	// prefetched holds the entries of the manifest this go command started
	// from.
	prefetched map[[actionIDSize]byte]struct{}
	used       map[[actionIDSize]byte]struct{}
	order      [][]byte

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
		c.remote = newGHAClient(opts.ResultsURL, opts.Token)
		c.uploads = make(chan upload, uploadQueueSize)
		for range opts.Concurrency {
			go c.uploadLoop()
		}
		if opts.ManifestKey != "" {
			go c.prefetch()
		}
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

func (c *Cache) manifestVersion() string {
	return c.opts.Namespace + "-manifest"
}

func (c *Cache) remoteUp() bool {
	return c.remote != nil && !c.remoteDown.Load()
}

func (c *Cache) markUsed(actionID []byte) {
	if len(actionID) != actionIDSize {
		return
	}
	c.usedMu.Lock()
	defer c.usedMu.Unlock()
	id := [actionIDSize]byte(actionID)
	if _, ok := c.used[id]; !ok {
		c.used[id] = struct{}{}
		c.order = append(c.order, id[:])
	}
}

// Get implements Handler.
func (c *Cache) Get(actionID []byte) (Entry, bool, error) {
	entry, ok := c.disk.get(actionID)
	if !ok && len(actionID) == actionIDSize && c.fetch(actionID) {
		entry, ok = c.disk.get(actionID)
	}
	if ok {
		c.markUsed(actionID)
	}
	return entry, ok, nil
}

// fetch downloads an entry to local disk, once even when a prefetch and the
// go command ask for it at the same time. A cache that fails or holds a
// corrupt entry must not fail the build: it reports a miss, and a failing
// cache is not asked again by this go command.
func (c *Cache) fetch(actionID []byte) bool {
	found, _, _ := c.fetches.Do(string(actionID), func() (any, error) {
		if _, ok := c.disk.get(actionID); ok {
			return true, nil
		}
		if !c.remoteUp() {
			return false, nil
		}
		body, ok, err := c.remote.open(c.ctx, c.version(actionID), c.key(actionID))
		if err != nil {
			c.remoteDown.Store(true)
			return false, nil
		}
		if !ok {
			return false, nil
		}
		defer func() { _ = body.Close() }()
		// The output goes from the response to local disk, so concurrent
		// prefetches hold no output in memory.
		header := make([]byte, headerSize)
		if _, err := io.ReadFull(body, header); err != nil {
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

// prefetch downloads the entries that the previous run of the same go command
// used, before the go command asks for them one at a time.
func (c *Cache) prefetch() {
	body, ok, err := c.remote.open(c.ctx, c.manifestVersion(), c.opts.ManifestKey, c.opts.ManifestKey+"-")
	if err != nil {
		c.remoteDown.Store(true)
		return
	}
	if !ok {
		return
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || len(data)%actionIDSize != 0 {
		return
	}
	prefetched := make(map[[actionIDSize]byte]struct{}, len(data)/actionIDSize)
	for i := 0; i < len(data); i += actionIDSize {
		prefetched[[actionIDSize]byte(data[i:i+actionIDSize])] = struct{}{}
	}
	c.usedMu.Lock()
	c.prefetched = prefetched
	c.usedMu.Unlock()
	sem := make(chan struct{}, c.opts.Concurrency)
	var wg sync.WaitGroup
	for i := 0; i < len(data) && c.remoteUp(); i += actionIDSize {
		actionID := data[i : i+actionIDSize]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			c.fetch(actionID)
		})
	}
	wg.Wait()
}

// Put implements Handler. The upload runs in the background; Close waits for
// it.
func (c *Cache) Put(actionID, outputID, body []byte) (string, error) {
	if len(outputID) != outputIDSize {
		return "", fmt.Errorf("output ID is %d bytes, want %d", len(outputID), outputIDSize)
	}
	stored := time.Now()
	path, err := c.disk.put(actionID, outputID, body, stored)
	if err != nil {
		return "", err
	}
	test := isTestRecord(body)
	if test && !c.opts.ShareTestResults {
		return path, nil
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
			if err := c.uploadEntry(u); err != nil && !errors.Is(err, errEntryExists) {
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
	return c.remote.upload(c.ctx, c.version(u.actionID), c.key(u.actionID), body, int64(len(header))+info.Size())
}

// Close implements Handler: within CloseTimeout, it waits for pending uploads,
// then records the entries this go command used for the next run to
// prefetch.
func (c *Cache) Close() error {
	defer c.cancel()
	if c.remote == nil {
		return nil
	}
	deadline := time.Now().Add(c.opts.CloseTimeout)
	done := make(chan struct{})
	go func() {
		c.uploadWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		return nil
	}
	if c.opts.ManifestKey != "" && c.remoteUp() {
		ctx, cancel := context.WithDeadline(c.ctx, deadline)
		defer cancel()
		c.uploadManifest(ctx)
	}
	return nil
}

// uploadManifest saves the entries this go command used, in the order it
// first used them, as a new entry: cache entries are immutable, and a lookup
// by prefix returns the newest. A go command that only used entries it
// prefetched saves nothing, so a build that keeps running unchanged does not
// add a manifest per run. The entries it no longer uses stay in the manifest
// until the next run that uses a new one.
func (c *Cache) uploadManifest(ctx context.Context) {
	c.usedMu.Lock()
	data := bytes.Join(c.order, nil)
	unchanged := true
	for id := range c.used {
		if _, ok := c.prefetched[id]; !ok {
			unchanged = false
			break
		}
	}
	c.usedMu.Unlock()
	if len(data) == 0 || unchanged {
		return
	}
	key := c.opts.ManifestKey + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_ = c.remote.upload(ctx, c.manifestVersion(), key, bytes.NewReader(data), int64(len(data)))
}
