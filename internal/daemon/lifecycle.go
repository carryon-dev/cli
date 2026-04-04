package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carryon-dev/cli/internal/ipc"
)

// GetBaseDir returns the base directory for carryon data.
// Uses CARRYON_BASE_DIR env var if set, otherwise defaults to ~/.carryon.
func GetBaseDir() string {
	if dir := os.Getenv("CARRYON_BASE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	return filepath.Join(home, ".carryon")
}

// GetSocketPath returns the IPC socket path for the given base directory.
// On Unix: baseDir/daemon.sock
// On Windows: \\.\pipe\carryon-{sha256hash[:12]}
func GetSocketPath(baseDir string) string {
	if runtime.GOOS == "windows" {
		h := sha256.Sum256([]byte(baseDir))
		hash := fmt.Sprintf("%x", h[:])[:12]
		return `\\.\pipe\carryon-` + hash
	}
	return filepath.Join(baseDir, "daemon.sock")
}

// EnsureBaseDir creates the base directory with 0700 permissions if it doesn't exist.
func EnsureBaseDir(baseDir string) error {
	return os.MkdirAll(baseDir, 0700)
}

// WritePidFile writes the current process PID to baseDir/daemon.pid.
func WritePidFile(baseDir string) error {
	pidPath := filepath.Join(baseDir, "daemon.pid")
	return os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600)
}

// RemovePidFile deletes baseDir/daemon.pid if it exists.
func RemovePidFile(baseDir string) {
	pidPath := filepath.Join(baseDir, "daemon.pid")
	os.Remove(pidPath)
}

// ReadPidFile reads and parses the PID from baseDir/daemon.pid.
func ReadPidFile(baseDir string) (int, error) {
	pidPath := filepath.Join(baseDir, "daemon.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in %s: %w", pidPath, err)
	}
	return pid, nil
}

// EnsureDaemon checks if a daemon is already running by trying to connect to the
// socket. If not, it forks a new daemon process (re-execing self with
// "daemon start --foreground") and polls the socket for up to 3 seconds.
func EnsureDaemon(baseDir string) error {
	if err := EnsureBaseDir(baseDir); err != nil {
		return fmt.Errorf("ensure base dir: %w", err)
	}
	socketPath := GetSocketPath(baseDir)

	// Try connecting to existing daemon.
	if tryConnect(socketPath) {
		return nil
	}

	// Clean up stale socket (Unix only - named pipes auto-clean on Windows).
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); err == nil {
			os.Remove(socketPath)
		}
	}

	// Fork daemon - re-exec ourselves with start --foreground.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	cmd := exec.Command(exe, "start", "--foreground")
	cmd.SysProcAttr = detachAttr()
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "CARRYON_DAEMON=1")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	// Detach the child so it survives our exit.
	_ = cmd.Process.Release()

	// Poll socket for up to 3 seconds (30 x 100ms).
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if tryConnect(socketPath) {
			return nil
		}
	}

	return fmt.Errorf("failed to start daemon (socket not ready after 3s)")
}

// StopDaemon reads the PID file, sends SIGTERM to the daemon process, and
// cleans up the socket and PID file.
func StopDaemon(baseDir string) error {
	pid, err := ReadPidFile(baseDir)
	if err == nil && pid > 0 {
		proc, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = proc.Signal(syscall.SIGTERM)
			// Wait for process to exit (up to 3s)
			for i := 0; i < 30; i++ {
				time.Sleep(100 * time.Millisecond)
				if err := proc.Signal(syscall.Signal(0)); err != nil {
					break // process exited
				}
			}
		}
	}

	// Clean up socket file (Unix only).
	socketPath := GetSocketPath(baseDir)
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); err == nil {
			os.Remove(socketPath)
		}
	}

	RemovePidFile(baseDir)
	return nil
}

// tryConnect attempts a quick connect to the socket (Unix domain socket or
// Windows named pipe) and returns true if successful.
func tryConnect(socketPath string) bool {
	conn, err := ipc.Dial(socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
