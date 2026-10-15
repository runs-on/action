//go:build windows

package mbx

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess        = 0x00000008
	createBreakawayFromJob = 0x01000000
)

// startDetached starts the helper detached from the step's console, so it
// outlives the step that started it, and returns its PID. It first asks to
// leave the step's job object too, which a job object may refuse.
func startDetached(newCmd func() *exec.Cmd) (int, error) {
	var err error
	for _, flags := range []uint32{
		syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess | createBreakawayFromJob,
		syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	} {
		cmd := newCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err = cmd.Start(); err == nil {
			pid := cmd.Process.Pid
			return pid, cmd.Process.Release()
		}
	}
	return 0, err
}

// stopHelper is the fallback for a helper that does not answer. Windows
// offers no cheap way to check that pid is still the helper rather than a
// process that reused its ID, so the helper is left to the runner, which ends
// the job's processes.
func stopHelper(int) error {
	return nil
}
