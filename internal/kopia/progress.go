package kopia

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kopia/kopia/snapshot/restore"
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

type simpleRestoreProgressReporter struct {
	mu          sync.Mutex
	lastOutput  time.Time // Use time.Time for throttling
	spinPhase   int
	linePrinted bool

	updateInterval time.Duration
	outputTarget   *os.File
}

func newSimpleRestoreProgressReporter(interval time.Duration, target *os.File) *simpleRestoreProgressReporter {
	return &simpleRestoreProgressReporter{
		updateInterval: interval,
		outputTarget:   target,
		// lastOutput will be zero value, which is fine for initial check
	}
}

func (srp *simpleRestoreProgressReporter) callback(_ context.Context, stats restore.Stats) {
	srp.mu.Lock()
	defer srp.mu.Unlock()

	now := time.Now()
	// Simple time-based throttling
	if now.Sub(srp.lastOutput) < srp.updateInterval && srp.linePrinted {
		return
	}
	srp.lastOutput = now // Update last output time

	spinnerChar := simpleRestoreSpinnerChars[srp.spinPhase%len(simpleRestoreSpinnerChars)]
	srp.spinPhase++

	// Format a status line similar to CLI progress, using our simple formatter
	statusLine := fmt.Sprintf(
		" %c Restored: %d files, %d dirs, %d symlinks (%s total). Skipped: %d files (%s). Errors: %d ignored.",
		spinnerChar,
		stats.RestoredFileCount,
		stats.RestoredDirCount,
		stats.RestoredSymlinkCount,
		formatBytesSimple(stats.RestoredTotalFileSize), // Use simple formatter
		stats.SkippedCount,
		formatBytesSimple(stats.SkippedTotalFileSize), // Use simple formatter
		stats.IgnoredErrorCount,
	)

	fmt.Fprintf(srp.outputTarget, "\r%-80s", statusLine)
	srp.linePrinted = true
}

func (srp *simpleRestoreProgressReporter) finish() {
	srp.mu.Lock()
	defer srp.mu.Unlock()

	if srp.linePrinted {
		// Clear the progress line and ensure cursor moves to next line
		fmt.Fprintf(srp.outputTarget, "\r%s\r\n", strings.Repeat(" ", 80)) // Clear line and add newline
		srp.linePrinted = false
	}
}
