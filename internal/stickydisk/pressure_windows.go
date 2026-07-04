//go:build windows

package stickydisk

import "fmt"

func statDisk(path string) (diskStats, error) {
	return diskStats{}, fmt.Errorf("disk stats not supported on windows")
}
