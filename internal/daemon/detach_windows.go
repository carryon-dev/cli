//go:build windows

package daemon

import "syscall"

// detachAttr returns SysProcAttr suitable for launching a detached daemon on Windows.
// CREATE_NEW_PROCESS_GROUP prevents the child from receiving console signals.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}
