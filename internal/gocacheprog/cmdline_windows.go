package gocacheprog

import (
	"os"
	"syscall"
	"unsafe"
)

var procNtQueryInformationProcess = syscall.NewLazyDLL("ntdll.dll").NewProc("NtQueryInformationProcess")

const (
	processQueryLimitedInformation = 0x1000
	// processCommandLineInformation asks NtQueryInformationProcess for a
	// UNICODE_STRING followed by the characters it points to.
	processCommandLineInformation = 60
)

// parentCommandLine returns the command line of the go command that started
// this helper, or nil when it cannot be read.
func parentCommandLine() []byte {
	process, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(os.Getppid()))
	if err != nil {
		return nil
	}
	defer func() { _ = syscall.CloseHandle(process) }()
	// A command line holds at most 32,767 UTF-16 characters.
	buf := make([]byte, 66<<10)
	status, _, _ := procNtQueryInformationProcess.Call(uintptr(process), processCommandLineInformation,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if status != 0 {
		return nil
	}
	line := (*struct {
		Length, MaximumLength uint16
		Buffer                *uint16
	})(unsafe.Pointer(&buf[0]))
	if line.Buffer == nil || int(line.Length) > len(buf) {
		return nil
	}
	return []byte(syscall.UTF16ToString(unsafe.Slice(line.Buffer, line.Length/2)))
}
