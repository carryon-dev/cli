//go:build !windows

package holder

import (
	"net"
	"os"
	"path/filepath"
)

// SocketPath returns the path for a holder's Unix domain socket.
func SocketPath(baseDir, sessionID string) string {
	return filepath.Join(baseDir, "holders", sessionID+".sock")
}

// Listen creates a Unix domain socket listener at the given path.
// It creates the parent directory if needed and removes any stale socket file.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	// Remove stale socket file if it exists.
	_ = os.Remove(path)

	return net.Listen("unix", path)
}

// Dial connects to a holder Unix domain socket.
func Dial(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// Cleanup removes the socket file.
func Cleanup(path string) {
	_ = os.Remove(path)
}
