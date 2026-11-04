//go:build !windows

package gocacheprog

import "syscall"

// detachedProcAttr puts the prefetch in its own session, so it outlives the
// action's step.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
