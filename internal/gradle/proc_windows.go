package gradle

import "syscall"

// detachedProcess is the DETACHED_PROCESS creation flag: no console.
const detachedProcess = 0x00000008

// detachedProcAttr keeps the server out of the action's console and process
// group, so it outlives the action's step.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
