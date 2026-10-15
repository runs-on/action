//go:build !windows

package mbx

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// startDetached starts the helper in its own session, so it outlives the
// step that started it, and returns its PID.
func startDetached(newCmd func() *exec.Cmd) (int, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

// stopHelper is the fallback for a helper that does not answer: SIGTERM, if
// pid still runs the helper.
func stopHelper(pid int) error {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(cmdline), "\x00"+Flag) {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
