//go:build ignore

// sign-checksums signs a checksums.txt file with the ed25519 private key
// from the RELEASE_SIGNING_KEY environment variable.
//
// Usage:
//
//	RELEASE_SIGNING_KEY=<base64-private-key> go run scripts/sign-checksums.go checksums.txt
//
// Produces checksums.txt.sig containing the base64-encoded signature.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: sign-checksums <checksums.txt>\n")
		os.Exit(1)
	}

	keyB64 := os.Getenv("RELEASE_SIGNING_KEY")
	if keyB64 == "" {
		fmt.Fprintf(os.Stderr, "RELEASE_SIGNING_KEY environment variable not set\n")
		os.Exit(1)
	}

	privKey, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid RELEASE_SIGNING_KEY: %v\n", err)
		os.Exit(1)
	}
	if len(privKey) != ed25519.PrivateKeySize {
		fmt.Fprintf(os.Stderr, "invalid RELEASE_SIGNING_KEY length: %d (expected %d)\n", len(privKey), ed25519.PrivateKeySize)
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(privKey), data)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	sigPath := os.Args[1] + ".sig"
	if err := os.WriteFile(sigPath, []byte(sigB64+"\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", sigPath, err)
		os.Exit(1)
	}

	fmt.Printf("Signed %s -> %s\n", os.Args[1], sigPath)
}
