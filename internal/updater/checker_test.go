package updater

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldCheckReturnsTrueWhenNoStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewChecker(tmpDir, "0.1.0")
	if !c.ShouldCheck() {
		t.Error("ShouldCheck should return true when no state file exists")
	}
}

func TestShouldCheckReturnsFalseWhenCheckedRecently(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewChecker(tmpDir, "0.1.0")

	// Write a recent check time
	c.RecordCheck()

	if c.ShouldCheck() {
		t.Error("ShouldCheck should return false when checked recently")
	}
}

func TestShouldCheckReturnsTrueWhenCheckedOverADayAgo(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewChecker(tmpDir, "0.1.0")

	// Write a stale check time (more than 24h ago)
	stateDir := filepath.Join(tmpDir, "state")
	os.MkdirAll(stateDir, 0700)
	data := updateCheckState{
		LastCheck: time.Now().Add(-25 * time.Hour),
	}
	b, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(stateDir, "update-check.json"), b, 0644)

	if !c.ShouldCheck() {
		t.Error("ShouldCheck should return true when last check was > 24h ago")
	}
}

func TestRecordCheckWritesStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	c := NewChecker(tmpDir, "0.1.0")

	c.RecordCheck()

	statePath := filepath.Join(tmpDir, "state", "update-check.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file should exist after RecordCheck: %v", err)
	}

	var state updateCheckState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state file should be valid JSON: %v", err)
	}

	if time.Since(state.LastCheck) > time.Minute {
		t.Error("LastCheck should be approximately now")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		newer   bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.2.0", "0.1.0", false},
		{"0.2.0", "0.2.0", false},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},
		{"0.10.0", "0.9.0", false},
		{"dev", "0.1.0", true},
		{"0.1.0", "dev", false},
	}

	for _, tt := range tests {
		result := isNewer(tt.latest, tt.current)
		if result != tt.newer {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, result, tt.newer)
		}
	}
}

// setupTestSigning generates a test ed25519 keypair and sets releasePublicKey
// for the duration of the test. Returns the private key for signing.
func setupTestSigning(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate test signing key: %v", err)
	}
	old := releasePublicKey
	releasePublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { releasePublicKey = old })
	return priv
}

// signChecksums signs checksumData with the given private key and returns
// the base64 signature string.
func signChecksums(t *testing.T, priv ed25519.PrivateKey, data []byte) string {
	t.Helper()
	sig := ed25519.Sign(priv, data)
	return base64.StdEncoding.EncodeToString(sig)
}

// TestVerifyChecksumMissingEntry verifies that verifyChecksum returns an error
// when the asset is not present in checksums.txt (only other platforms are listed).
func TestVerifyChecksumMissingEntry(t *testing.T) {
	priv := setupTestSigning(t)

	checksumBody := []byte("abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890  carryon-linux-amd64\n" +
		"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef  carryon-windows-amd64.exe\n")
	sig := signChecksums(t, priv, checksumBody)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt.sig" {
			w.Write([]byte(sig))
		} else {
			w.Write(checksumBody)
		}
	}))
	defer srv.Close()

	hasher := sha256.New()
	hasher.Write([]byte("some binary content"))

	_, _, err := verifyChecksum(hasher, "carryon-darwin-arm64", srv.URL+"/checksums.txt", srv.URL+"/checksums.txt.sig", srv.Client())
	if err == nil {
		t.Fatal("expected error when asset not found in checksums.txt, got nil")
	}
}

// TestVerifyChecksumMatchFound verifies that verifyChecksum succeeds when
// the asset entry exists, the signature is valid, and the hash matches.
func TestVerifyChecksumMatchFound(t *testing.T) {
	priv := setupTestSigning(t)

	content := []byte("binary content")
	h := sha256.New()
	h.Write(content)
	expectedHash := hex.EncodeToString(h.Sum(nil))

	checksumBody := []byte(expectedHash + "  carryon-darwin-arm64\n")
	sig := signChecksums(t, priv, checksumBody)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt.sig" {
			w.Write([]byte(sig))
		} else {
			w.Write(checksumBody)
		}
	}))
	defer srv.Close()

	hasher := sha256.New()
	hasher.Write(content)

	_, _, err := verifyChecksum(hasher, "carryon-darwin-arm64", srv.URL+"/checksums.txt", srv.URL+"/checksums.txt.sig", srv.Client())
	if err != nil {
		t.Fatalf("expected nil error for matching checksum, got: %v", err)
	}
}

// TestVerifyChecksumBadSignature verifies that verifyChecksum rejects
// checksums.txt when the signature doesn't match (tampered content).
func TestVerifyChecksumBadSignature(t *testing.T) {
	priv := setupTestSigning(t)

	content := []byte("binary content")
	h := sha256.New()
	h.Write(content)
	expectedHash := hex.EncodeToString(h.Sum(nil))

	// Sign the original checksums
	originalBody := []byte(expectedHash + "  carryon-darwin-arm64\n")
	sig := signChecksums(t, priv, originalBody)

	// Serve tampered checksums with the original signature
	tamperedBody := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  carryon-darwin-arm64\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt.sig" {
			w.Write([]byte(sig))
		} else {
			w.Write(tamperedBody)
		}
	}))
	defer srv.Close()

	hasher := sha256.New()
	hasher.Write(content)

	_, _, err := verifyChecksum(hasher, "carryon-darwin-arm64", srv.URL+"/checksums.txt", srv.URL+"/checksums.txt.sig", srv.Client())
	if err == nil {
		t.Fatal("expected error for tampered checksums.txt, got nil")
	}
	if !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("expected signature verification error, got: %v", err)
	}
}

// TestVerifyChecksumHashMismatch verifies that verifyChecksum rejects
// a binary whose hash doesn't match the signed checksum.
func TestVerifyChecksumHashMismatch(t *testing.T) {
	priv := setupTestSigning(t)

	checksumBody := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  carryon-darwin-arm64\n")
	sig := signChecksums(t, priv, checksumBody)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt.sig" {
			w.Write([]byte(sig))
		} else {
			w.Write(checksumBody)
		}
	}))
	defer srv.Close()

	hasher := sha256.New()
	hasher.Write([]byte("different binary content"))

	_, _, err := verifyChecksum(hasher, "carryon-darwin-arm64", srv.URL+"/checksums.txt", srv.URL+"/checksums.txt.sig", srv.Client())
	if err == nil {
		t.Fatal("expected error for hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
}

func TestBuildAssetName(t *testing.T) {
	name := buildAssetName("darwin", "arm64")
	if name != "carryon-darwin-arm64" {
		t.Errorf("expected carryon-darwin-arm64, got %s", name)
	}

	name = buildAssetName("windows", "amd64")
	if name != "carryon-windows-amd64.exe" {
		t.Errorf("expected carryon-windows-amd64.exe, got %s", name)
	}

	name = buildAssetName("linux", "amd64")
	if name != "carryon-linux-amd64" {
		t.Errorf("expected carryon-linux-amd64, got %s", name)
	}
}
