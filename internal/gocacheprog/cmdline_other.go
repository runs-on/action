//go:build !windows

package gocacheprog

import (
	"fmt"
	"os"
)

// parentCommandLine returns the command line of the go command that started
// this helper, or nil when it cannot be read: only Linux has /proc.
func parentCommandLine() []byte {
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", os.Getppid()))
	return cmdline
}
