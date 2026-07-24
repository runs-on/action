//go:build !windows

package stickydisk

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

func processIsGitProxy(pid int) bool {
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		return commandLineHasGitProxyServe(string(cmdline))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return err == nil && commandLineHasGitProxyServe(string(out))
}
