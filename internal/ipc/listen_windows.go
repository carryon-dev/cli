//go:build windows

package ipc

import (
	"net"

	winio "github.com/Microsoft/go-winio"
)

// Listen creates a Windows named pipe listener at addr.
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, nil)
}

// ListenSecure creates a Windows named pipe listener at addr.
// On Windows, named pipes are secured by ACLs rather than umask, so this
// is equivalent to Listen.
func ListenSecure(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, nil)
}

// Dial connects to a Windows named pipe at addr.
func Dial(addr string) (net.Conn, error) {
	return winio.DialPipe(addr, nil)
}
