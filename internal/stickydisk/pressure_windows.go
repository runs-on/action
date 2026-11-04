//go:build windows

package stickydisk

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func statDisk(path string) (diskStats, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return diskStats{}, err
	}
	var freeAvailable, total, totalFree uint64
	ret, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return diskStats{}, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, callErr)
	}
	// NTFS has no fixed inode table; zero totals read as 100%% free inodes.
	return diskStats{totalBytes: total, freeBytes: freeAvailable}, nil
}
