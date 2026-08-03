//go:build !windows

package stickydisk

import (
	"errors"
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

func forceKillGitProxy(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("get git proxy process group: %w", err)
	}
	if pgid != pid {
		return fmt.Errorf("refusing to kill process group %d for git proxy pid %d", pgid, pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill git proxy process group %d: %w", pid, err)
	}
	return nil
}

func processIsGitProxy(pid int) bool {
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		return commandLineHasGitProxyServe(string(cmdline))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return err == nil && commandLineHasGitProxyServe(string(out))
}
