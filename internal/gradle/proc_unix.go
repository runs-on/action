//go:build !windows

package gradle

import "syscall"

// detachedProcAttr puts the server in its own session, so it outlives the
// action's step.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
