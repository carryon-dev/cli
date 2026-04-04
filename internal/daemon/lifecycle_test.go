package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGetBaseDir(t *testing.T) {
	dir := GetBaseDir()
	if !strings.HasSuffix(dir, ".carryon") {
		t.Errorf("GetBaseDir() = %q, want suffix .carryon", dir)
	}
	// Should be an absolute path
	if !filepath.IsAbs(dir) {
		t.Errorf("GetBaseDir() = %q, want absolute path", dir)
	}
}

func TestGetSocketPath(t *testing.T) {
	base := "/tmp/test-carryon"
	sock := GetSocketPath(base)
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(sock, `\\.\pipe\carryon-`) {
			t.Errorf("GetSocketPath(%q) = %q, want prefix \\\\.\\pipe\\carryon-", base, sock)
		}
	} else {
		if !strings.HasSuffix(sock, "daemon.sock") {
			t.Errorf("GetSocketPath(%q) = %q, want suffix daemon.sock", base, sock)
		}
	}
}

func TestPidFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Write PID file
	if err := WritePidFile(tmpDir); err != nil {
		t.Fatalf("WritePidFile() error = %v", err)
	}

	// Read PID file
	pid, err := ReadPidFile(tmpDir)
	if err != nil {
		t.Fatalf("ReadPidFile() error = %v", err)
	}

	// Should match our own PID
	if pid != os.Getpid() {
		t.Errorf("ReadPidFile() = %d, want %d", pid, os.Getpid())
	}

	// Remove PID file
	RemovePidFile(tmpDir)

	// Should be gone
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("PID file still exists after RemovePidFile()")
	}

	// ReadPidFile on missing file should return error
	_, err = ReadPidFile(tmpDir)
	if err == nil {
		t.Errorf("ReadPidFile() on missing file should return error")
	}
}

func TestEnsureBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "nested", "carryon-test")

	if err := EnsureBaseDir(baseDir); err != nil {
		t.Fatalf("EnsureBaseDir() error = %v", err)
	}

	info, err := os.Stat(baseDir)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", baseDir, err)
	}
	if !info.IsDir() {
		t.Errorf("EnsureBaseDir() did not create a directory")
	}

	// Check permissions (Unix-only; Windows always reports 0777)
	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		if perm != 0700 {
			t.Errorf("EnsureBaseDir() dir perms = %o, want 0700", perm)
		}
	}

	// Calling again should not fail
	if err := EnsureBaseDir(baseDir); err != nil {
		t.Errorf("EnsureBaseDir() second call error = %v", err)
	}
}
