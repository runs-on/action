//go:build !windows

package stickydisk

import (
	"fmt"
	"syscall"
)

func statDisk(path string) (diskStats, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskStats{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return diskStats{
		totalBytes:  st.Blocks * uint64(st.Bsize),
		freeBytes:   st.Bavail * uint64(st.Bsize),
		totalInodes: st.Files,
		freeInodes:  st.Ffree,
	}, nil
}
