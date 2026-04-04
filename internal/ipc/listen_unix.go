//go:build !windows

package ipc

import (
	"net"
	"syscall"
)

// Listen creates a Unix domain socket listener at addr.
func Listen(addr string) (net.Listener, error) { return net.Listen("unix", addr) }

// ListenSecure creates a Unix domain socket listener at addr with the umask
// set to 0077 during creation so the socket is owner-only from the start,
// eliminating the race window between socket creation and a subsequent chmod.
func ListenSecure(addr string) (net.Listener, error) {
	oldMask := syscall.Umask(0077)
	ln, err := net.Listen("unix", addr)
	syscall.Umask(oldMask)
	return ln, err
}

// Dial connects to a Unix domain socket at addr.
func Dial(addr string) (net.Conn, error) { return net.Dial("unix", addr) }
