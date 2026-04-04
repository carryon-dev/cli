package crypto

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/curve25519"
)

// GenerateKeypair generates a new X25519 keypair using crypto/rand.
// Returns the 32-byte public key and 32-byte private key.
func GenerateKeypair() (pub, priv []byte, err error) {
	priv = make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return nil, nil, fmt.Errorf("generating private key: %w", err)
	}

	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("deriving public key: %w", err)
	}

	return pub, priv, nil
}

// SaveKeypair writes a keypair to the given directory.
// Creates the directory with permissions 0700 if it doesn't exist.
// Writes device.key (0600) for the private key and device.pub (0644) for the public key.
func SaveKeypair(dir string, pub, priv []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	privPath := filepath.Join(dir, "device.key")
	if err := os.WriteFile(privPath, priv, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	pubPath := filepath.Join(dir, "device.pub")
	if err := os.WriteFile(pubPath, pub, 0644); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	return nil
}

// LoadKeypair reads a keypair from the given directory.
// Expects device.key and device.pub files to exist.
// Returns an error if either file is missing.
func LoadKeypair(dir string) (pub, priv []byte, err error) {
	privPath := filepath.Join(dir, "device.key")
	priv, err = os.ReadFile(privPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading private key: %w", err)
	}

	pubPath := filepath.Join(dir, "device.pub")
	pub, err = os.ReadFile(pubPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading public key: %w", err)
	}

	if len(priv) != 32 {
		return nil, nil, fmt.Errorf("private key file has wrong length: %d bytes (expected 32)", len(priv))
	}
	if len(pub) != 32 {
		return nil, nil, fmt.Errorf("public key file has wrong length: %d bytes (expected 32)", len(pub))
	}

	return pub, priv, nil
}
