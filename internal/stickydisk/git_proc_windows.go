//go:build windows

package stickydisk

import (
	"os"
	"syscall"
)

// The git cache mode is Linux-only (modes with a Setup function are skipped
// on Windows by supportedOn); these stubs only keep the package compiling.

func detachedProcAttr() *syscall.SysProcAttr {
	return nil
}

func signalTerm(proc *os.Process) error {
	return proc.Kill()
}

func processAlive(proc *os.Process) bool {
	return false
}

func forceKillGitProxy(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	return proc.Kill()
}

func processIsGitProxy(int) bool {
	return false
}
