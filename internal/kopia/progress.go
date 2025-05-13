package kopia

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/restore"
	"github.com/kopia/kopia/snapshot/snapshotfs"
)

const simpleRestoreSpinnerChars = "|/-\\"

// Basic byte formatter
func formatBytesSimple(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// snapshotProgressAdapter adapts upload.Progress events to a callback expecting restore.Stats.
type snapshotProgressAdapter struct {
	mu         sync.Mutex
	ctx        context.Context
	cb         func(context.Context, snapshot.Stats)
	lastUpdate time.Time
	interval   time.Duration

	// Counters for synthesizing basic restore.Stats fields
	totalSnapshotFiles int64 // Sum of cached and new/hashed files
	totalSnapshotBytes int64 // Sum of their sizes
	excludedFiles      int64
	excludedBytes      int64
	totalErrors        int64 // Sum of ignored and fatal errors
}

type restoreProgressAdapter struct {
	mu                 sync.Mutex
	ctx                context.Context
	cb                 func(context.Context, restore.Stats)
	lastUpdate         time.Time
	interval           time.Duration
	totalRestoredFiles int64
	totalRestoredBytes int64
}

// newSnapshotProgressAdapter creates a new adapter.
func newSnapshotProgressAdapter(ctx context.Context, callback func(context.Context, snapshot.Stats), updateInterval time.Duration) *snapshotProgressAdapter {
	return &snapshotProgressAdapter{
		ctx:      ctx,
		cb:       callback,
		interval: updateInterval,
	}
}

func newRestoreProgressAdapter(ctx context.Context, callback func(context.Context, restore.Stats), updateInterval time.Duration) *restoreProgressAdapter {
	return &restoreProgressAdapter{
		ctx:      ctx,
		cb:       callback,
		interval: updateInterval,
	}
}

func (a *snapshotProgressAdapter) triggerUpdate() {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if now.Sub(a.lastUpdate) < a.interval && atomic.LoadInt64(&a.totalSnapshotFiles) > 0 {
		return
	}
	a.lastUpdate = now

	stats := snapshot.Stats{
		TotalFileCount:        int32(atomic.LoadInt64(&a.totalSnapshotFiles)),
		TotalFileSize:         atomic.LoadInt64(&a.totalSnapshotBytes),
		ExcludedFileCount:     int32(atomic.LoadInt64(&a.excludedFiles)),
		ExcludedTotalFileSize: atomic.LoadInt64(&a.excludedBytes),
		IgnoredErrorCount:     int32(atomic.LoadInt64(&a.totalErrors)), // Consolidating all errors here
	}
	a.cb(a.ctx, stats)
}

func (a *restoreProgressAdapter) triggerUpdate() {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if now.Sub(a.lastUpdate) < a.interval && atomic.LoadInt64(&a.totalRestoredFiles) > 0 {
		return
	}
	a.lastUpdate = now

	stats := restore.Stats{
		RestoredFileCount:     int32(atomic.LoadInt64(&a.totalRestoredFiles)),
		RestoredTotalFileSize: atomic.LoadInt64(&a.totalRestoredBytes),
	}
	a.cb(a.ctx, stats)
}

// upload.Progress interface implementation
func (a *snapshotProgressAdapter) Enabled() bool { return true }

func (a *snapshotProgressAdapter) UploadStarted() {
	atomic.StoreInt64(&a.totalSnapshotFiles, 0)
	atomic.StoreInt64(&a.totalSnapshotBytes, 0)
	atomic.StoreInt64(&a.excludedFiles, 0)
	atomic.StoreInt64(&a.excludedBytes, 0)
	atomic.StoreInt64(&a.totalErrors, 0)
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) UploadFinished() {
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) CachedFile(path string, size int64) {
	atomic.AddInt64(&a.totalSnapshotFiles, 1)
	atomic.AddInt64(&a.totalSnapshotBytes, size)
}

func (a *snapshotProgressAdapter) HashingFile(fname string) {}

func (a *snapshotProgressAdapter) ExcludedFile(fname string, size int64) {
	atomic.AddInt64(&a.excludedFiles, 1)
	atomic.AddInt64(&a.excludedBytes, size)
}

func (a *snapshotProgressAdapter) ExcludedDir(dirname string) {}

func (a *snapshotProgressAdapter) FinishedHashingFile(fname string, numBytes int64) {
	atomic.AddInt64(&a.totalSnapshotFiles, 1)
	atomic.AddInt64(&a.totalSnapshotBytes, numBytes)
}

func (a *snapshotProgressAdapter) FinishedFile(fname string, err error) {
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) HashedBytes(numBytes int64) {
	// This indicates activity, can trigger update. Bytes are part of file total.
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) Error(path string, err error, isIgnored bool) {
	atomic.AddInt64(&a.totalErrors, 1)
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) UploadedBytes(numBytes int64) {
	// restore.Stats doesn't have a direct field for this if it differs from total file sizes.
	// We can trigger an update as it signifies progress.
	a.triggerUpdate()
}

func (a *snapshotProgressAdapter) StartedDirectory(dirname string)  {}
func (a *snapshotProgressAdapter) FinishedDirectory(dirname string) {}

func (a *snapshotProgressAdapter) EstimationParameters() snapshotfs.EstimationParameters {
	return snapshotfs.EstimationParameters{
		Type: snapshotfs.EstimationTypeClassic,
	}
}

func (a *snapshotProgressAdapter) EstimatedDataSize(fileCount int64, totalBytes int64) {
	// No direct fields in the simplified restore.Stats mapping.
	// Still, trigger an update as it might be start of a new phase.
	a.triggerUpdate()
}

// इंश्योर snapshotProgressAdapter implements upload.Progress
// var _ upload.Progress = (*snapshotProgressAdapter)(nil)

type simpleRestoreProgressReporter struct {
	mu             sync.Mutex
	lastOutput     time.Time
	spinPhase      int
	linePrinted    bool
	updateInterval time.Duration
	outputTarget   *os.File
}

func newSimpleRestoreProgressReporter(interval time.Duration, target *os.File) *simpleRestoreProgressReporter {
	return &simpleRestoreProgressReporter{
		updateInterval: interval,
		outputTarget:   target,
	}
}

func (srp *simpleRestoreProgressReporter) callbackRestore(_ context.Context, stats restore.Stats) {
	srp.mu.Lock()
	defer srp.mu.Unlock()

	now := time.Now()
	if now.Sub(srp.lastOutput) < srp.updateInterval && srp.linePrinted {
		return
	}
	srp.lastOutput = now

	spinnerChar := simpleRestoreSpinnerChars[srp.spinPhase%len(simpleRestoreSpinnerChars)]
	srp.spinPhase++

	statusMsg := fmt.Sprintf(
		" %c Snapshotted: %d files (%s). Excl: %d files (%s). Errors: %d.",
		spinnerChar,
		stats.RestoredFileCount, // Total files in snapshot
		formatBytesSimple(stats.RestoredTotalFileSize), // Total size of those files
		stats.SkippedCount, // Excluded files
		formatBytesSimple(stats.SkippedTotalFileSize),
		stats.IgnoredErrorCount, // Total errors
	)

	fmt.Fprintf(srp.outputTarget, "\r%-90s", statusMsg) // Adjusted width
	srp.linePrinted = true
}

func (srp *simpleRestoreProgressReporter) callbackSnapshot(_ context.Context, stats snapshot.Stats) {
	srp.mu.Lock()
	defer srp.mu.Unlock()

	now := time.Now()
	if now.Sub(srp.lastOutput) < srp.updateInterval && srp.linePrinted {
		return
	}
	srp.lastOutput = now

	spinnerChar := simpleRestoreSpinnerChars[srp.spinPhase%len(simpleRestoreSpinnerChars)]
	srp.spinPhase++

	statusMsg := fmt.Sprintf(
		" %c Snapshotted: %d files (%s). Excl: %d files (%s). Errors: %d.",
		spinnerChar,
		stats.TotalFileCount,                   // Total files in snapshot
		formatBytesSimple(stats.TotalFileSize), // Total size of those files
		stats.ExcludedFileCount,                // Excluded files
		formatBytesSimple(stats.ExcludedTotalFileSize),
		stats.IgnoredErrorCount, // Total errors
	)

	fmt.Fprintf(srp.outputTarget, "\r%-90s", statusMsg) // Adjusted width
	srp.linePrinted = true
}

func (srp *simpleRestoreProgressReporter) finish() {
	srp.mu.Lock()
	defer srp.mu.Unlock()

	if srp.linePrinted {
		fmt.Fprintf(srp.outputTarget, "\r%s\r\n", strings.Repeat(" ", 90))
		srp.linePrinted = false
	}
}
