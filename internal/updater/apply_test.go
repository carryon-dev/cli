package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApplyReplacesBinary(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake "current" binary
	currentDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(currentDir, 0755)
	currentPath := filepath.Join(currentDir, "carryon")
	os.WriteFile(currentPath, []byte("old-binary"), 0755)

	// Create a fake "new" binary in updates dir
	updatesDir := filepath.Join(tmpDir, "updates")
	os.MkdirAll(updatesDir, 0755)
	assetName := fmt.Sprintf("carryon-%s-%s", runtime.GOOS, runtime.GOARCH)
	newPath := filepath.Join(updatesDir, assetName)
	os.WriteFile(newPath, []byte("new-binary"), 0755)

	// Set ExpectedHash to exercise the same-session verification path.
	h := sha256.Sum256([]byte("new-binary"))
	a := NewApplier(tmpDir)
	a.ExpectedHash = hex.EncodeToString(h[:])
	err := a.ApplyTo(newPath, currentPath)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Read the current path - it should have the new content
	content, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("failed to read replaced binary: %v", err)
	}
	if string(content) != "new-binary" {
		t.Errorf("expected 'new-binary', got %q", string(content))
	}
}

func TestCleanupOldBinaryRemovesBakFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a .bak file next to a fake executable
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	exePath := filepath.Join(binDir, "carryon")
	os.WriteFile(exePath, []byte("binary"), 0755)
	bakPath := filepath.Join(binDir, "carryon.bak")
	os.WriteFile(bakPath, []byte("old-binary"), 0755)

	a := NewApplier(tmpDir)
	a.CleanupOldBinaryAt(exePath)

	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Error(".bak file should have been removed")
	}
}

func TestCleanupOldBinaryRemovesOldExeFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a .old.exe file next to a fake executable
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	exePath := filepath.Join(binDir, "carryon.exe")
	os.WriteFile(exePath, []byte("binary"), 0755)
	oldPath := filepath.Join(binDir, "carryon.old.exe")
	os.WriteFile(oldPath, []byte("old-binary"), 0755)

	a := NewApplier(tmpDir)
	a.CleanupOldBinaryAt(exePath)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error(".old.exe file should have been removed")
	}
}

func TestHasPendingUpdateDetectsDownloadedBinary(t *testing.T) {
	tmpDir := t.TempDir()

	// No updates directory yet
	a := NewApplier(tmpDir)
	_, found := a.HasPendingUpdate()
	if found {
		t.Error("should not find pending update when updates dir doesn't exist")
	}

	// Create updates directory with a binary
	updatesDir := filepath.Join(tmpDir, "updates")
	os.MkdirAll(updatesDir, 0755)
	assetName := fmt.Sprintf("carryon-%s-%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := filepath.Join(updatesDir, assetName)
	os.WriteFile(binaryPath, []byte("new-binary"), 0755)

	path, found := a.HasPendingUpdate()
	if !found {
		t.Error("should find pending update when binary exists in updates dir")
	}
	if path != binaryPath {
		t.Errorf("expected path %s, got %s", binaryPath, path)
	}
}

func TestHasPendingUpdateIgnoresEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	updatesDir := filepath.Join(tmpDir, "updates")
	os.MkdirAll(updatesDir, 0755)

	a := NewApplier(tmpDir)
	_, found := a.HasPendingUpdate()
	if found {
		t.Error("should not find pending update when updates dir is empty")
	}
}
