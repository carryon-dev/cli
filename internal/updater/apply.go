package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Applier handles applying downloaded updates and cleaning up old binaries.
type Applier struct {
	baseDir      string
	ExpectedHash string // hex-encoded SHA-256 hash to verify before applying
}

// NewApplier creates a new Applier.
func NewApplier(baseDir string) *Applier {
	return &Applier{baseDir: baseDir}
}

// Apply replaces the currently running binary with the downloaded update.
// It gets the current executable path via os.Executable().
func (a *Applier) Apply(binaryPath string) error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine current executable path: %w", err)
	}
	// Resolve symlinks to get the real path
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}
	return a.ApplyTo(binaryPath, currentExe)
}

// ApplyTo replaces the binary at targetPath with the one at binaryPath.
// This is separated from Apply for testability.
// The binary is always re-verified before copying - either via ExpectedHash
// (for same-session updates) or via saved checksums.txt + ed25519 signature
// (for previously background-downloaded updates).
func (a *Applier) ApplyTo(binaryPath, targetPath string) error {
	if a.ExpectedHash != "" {
		// Same-session path: re-verify SHA-256 against the hash from Download().
		f, err := os.Open(binaryPath)
		if err != nil {
			return fmt.Errorf("failed to open binary for hash verification: %w", err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return fmt.Errorf("failed to hash binary: %w", err)
		}
		f.Close()
		actual := hex.EncodeToString(h.Sum(nil))
		if actual != a.ExpectedHash {
			return fmt.Errorf("binary hash mismatch before apply: expected %s, got %s", a.ExpectedHash, actual)
		}
	} else {
		// Background-download path: re-verify using saved checksums.txt
		// and its ed25519 signature. The public key is embedded in this
		// binary, so a local attacker cannot forge a valid signature.
		if err := a.verifyFromSavedChecksums(binaryPath); err != nil {
			return fmt.Errorf("pre-apply signature verification failed: %w", err)
		}
	}

	// Determine backup path
	bakPath := targetPath + ".bak"
	if runtime.GOOS == "windows" {
		// On Windows, rename running exe to .old.exe
		ext := filepath.Ext(targetPath)
		bakPath = strings.TrimSuffix(targetPath, ext) + ".old" + ext
	}

	// Rename current binary to backup
	if err := os.Rename(targetPath, bakPath); err != nil {
		return fmt.Errorf("failed to backup current binary: %w", err)
	}

	// Copy new binary to target path
	src, err := os.Open(binaryPath)
	if err != nil {
		// Try to restore backup
		os.Rename(bakPath, targetPath)
		return fmt.Errorf("failed to open new binary: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		// Try to restore backup
		os.Rename(bakPath, targetPath)
		return fmt.Errorf("failed to create target binary: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(targetPath)
		os.Rename(bakPath, targetPath)
		return fmt.Errorf("failed to copy new binary: %w", err)
	}

	// Close dst explicitly before cleanup - must happen before rename/remove.
	// src is closed by its defer above.
	if err := dst.Close(); err != nil {
		os.Remove(targetPath)
		os.Rename(bakPath, targetPath)
		return fmt.Errorf("failed to finalize target binary: %w", err)
	}

	// Remove backup, downloaded binary, and saved verification files
	os.Remove(bakPath)
	os.Remove(binaryPath)
	updatesDir := filepath.Dir(binaryPath)
	os.Remove(filepath.Join(updatesDir, "checksums.txt"))
	os.Remove(filepath.Join(updatesDir, "checksums.txt.sig"))

	return nil
}

// verifyFromSavedChecksums re-verifies a previously downloaded binary using
// the checksums.txt and checksums.txt.sig saved alongside it during download.
func (a *Applier) verifyFromSavedChecksums(binaryPath string) error {
	updatesDir := filepath.Dir(binaryPath)

	checksumData, err := os.ReadFile(filepath.Join(updatesDir, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("saved checksums.txt not found - cannot verify binary: %w", err)
	}

	sigData, err := os.ReadFile(filepath.Join(updatesDir, "checksums.txt.sig"))
	if err != nil {
		return fmt.Errorf("saved checksums.txt.sig not found - cannot verify binary: %w", err)
	}

	// Verify the ed25519 signature on checksums.txt using the embedded public key.
	if err := verifySignatureBytes(checksumData, sigData); err != nil {
		return err
	}

	// Hash the binary and check against the signed checksums.
	f, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("failed to open binary: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return fmt.Errorf("failed to hash binary: %w", err)
	}
	f.Close()
	actualHash := hex.EncodeToString(h.Sum(nil))

	assetName := filepath.Base(binaryPath)
	for _, line := range strings.Split(string(checksumData), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			if actualHash != parts[0] {
				return fmt.Errorf("binary hash mismatch: expected %s, got %s", parts[0], actualHash)
			}
			return nil
		}
	}

	return fmt.Errorf("asset %q not found in saved checksums.txt", assetName)
}

// CleanupOldBinary removes any .bak or .old.exe files next to the currently running executable.
func (a *Applier) CleanupOldBinary() {
	currentExe, err := os.Executable()
	if err != nil {
		return
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return
	}
	a.CleanupOldBinaryAt(currentExe)
}

// CleanupOldBinaryAt removes .bak and .old.exe files next to the given executable path.
func (a *Applier) CleanupOldBinaryAt(exePath string) {
	dir := filepath.Dir(exePath)
	base := filepath.Base(exePath)

	// Remove .bak file
	bakPath := filepath.Join(dir, base+".bak")
	os.Remove(bakPath)

	// Remove .old.exe file (Windows pattern)
	ext := filepath.Ext(base)
	if ext != "" {
		oldPath := filepath.Join(dir, strings.TrimSuffix(base, ext)+".old"+ext)
		os.Remove(oldPath)
	} else {
		oldPath := filepath.Join(dir, base+".old.exe")
		os.Remove(oldPath)
	}
}

// HasPendingUpdate checks if there's a downloaded binary in the updates directory.
// Returns the path to the binary and whether one was found.
func (a *Applier) HasPendingUpdate() (string, bool) {
	updatesDir := filepath.Join(a.baseDir, "updates")
	entries, err := os.ReadDir(updatesDir)
	if err != nil {
		return "", false
	}

	// Only match the expected asset for the current platform
	expectedName := buildAssetName(runtime.GOOS, runtime.GOARCH)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Name() == expectedName {
			return filepath.Join(updatesDir, expectedName), true
		}
	}

	return "", false
}
