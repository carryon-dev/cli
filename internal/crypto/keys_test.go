package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeypair(t *testing.T) {
	pub, priv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}
	if len(pub) != 32 {
		t.Errorf("pub key length = %d, want 32", len(pub))
	}
	if len(priv) != 32 {
		t.Errorf("priv key length = %d, want 32", len(priv))
	}
}

func TestKeypairDeterministic(t *testing.T) {
	pub1, priv1, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("first GenerateKeypair() error: %v", err)
	}
	pub2, priv2, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("second GenerateKeypair() error: %v", err)
	}

	// Two calls should produce different keys
	if string(pub1) == string(pub2) {
		t.Error("two generated public keys are identical - expected different random keys")
	}
	if string(priv1) == string(priv2) {
		t.Error("two generated private keys are identical - expected different random keys")
	}
}

func TestSaveAndLoadKeypair(t *testing.T) {
	dir := t.TempDir()

	pub, priv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() error: %v", err)
	}

	if err := SaveKeypair(dir, pub, priv); err != nil {
		t.Fatalf("SaveKeypair() error: %v", err)
	}

	// Verify private key file permissions are 0600
	privPath := filepath.Join(dir, "device.key")
	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat device.key error: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("device.key permissions = %o, want 0600", info.Mode().Perm())
	}

	// Verify public key file permissions are 0644
	pubPath := filepath.Join(dir, "device.pub")
	info, err = os.Stat(pubPath)
	if err != nil {
		t.Fatalf("stat device.pub error: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("device.pub permissions = %o, want 0644", info.Mode().Perm())
	}

	// Load back and compare
	loadedPub, loadedPriv, err := LoadKeypair(dir)
	if err != nil {
		t.Fatalf("LoadKeypair() error: %v", err)
	}
	if string(loadedPub) != string(pub) {
		t.Error("loaded public key does not match saved public key")
	}
	if string(loadedPriv) != string(priv) {
		t.Error("loaded private key does not match saved private key")
	}
}

func TestLoadKeypairNotFound(t *testing.T) {
	dir := t.TempDir()

	_, _, err := LoadKeypair(dir)
	if err == nil {
		t.Error("LoadKeypair() expected error when keys don't exist, got nil")
	}
}
