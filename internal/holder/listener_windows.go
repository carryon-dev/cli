//go:build windows

package holder

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// SocketPath returns the named pipe path for a holder on Windows.
func SocketPath(baseDir, sessionID string) string {
	return `\\.\pipe\carryon-holder-` + sessionID
}

// Listen creates a Windows named pipe listener at the given path.
func Listen(path string) (net.Listener, error) {
	return winio.ListenPipe(path, nil)
}

// Dial connects to a holder named pipe on Windows.
func Dial(path string) (net.Conn, error) {
	return winio.DialPipe(path, (*time.Duration)(nil))
}

// Cleanup is a no-op on Windows; named pipes are cleaned up by the OS.
func Cleanup(path string) {}
