//go:build !windows

package daemon

import "syscall"

// detachAttr returns SysProcAttr suitable for launching a detached daemon on Unix.
// Setsid creates a new session so the child survives parent exit.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
