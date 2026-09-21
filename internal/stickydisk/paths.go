package stickydisk

import (
	"os"
	"path/filepath"
)

// runnerTempPath places per-job state under RUNNER_TEMP, which the runner
// wipes between jobs. Predictable paths in a shared /tmp would let an earlier
// occupant of a reused runner pre-create files or symlinks at them.
func runnerTempPath(elem ...string) string {
	base := os.Getenv("RUNNER_TEMP")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(append([]string{base}, elem...)...)
}

func buildkitPreparedStateFile() string {
	return runnerTempPath("runs-on-buildkit-volume")
}

// writeFileFresh replaces path with a newly created regular file, never
// writing through a pre-existing file or symlink at that path.
func writeFileFresh(path string, data []byte, perm os.FileMode) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
