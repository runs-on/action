//go:build !windows

package stickydisk

import (
	"os"
	"syscall"
)

// detachedProcAttr puts the git proxy in its own session so it survives the
// action step's process group.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func signalTerm(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

// processAlive probes the process with signal 0.
func processAlive(proc *os.Process) bool {
	return proc.Signal(syscall.Signal(0)) == nil
}
