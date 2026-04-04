//go:build ignore

// generate-signing-key generates an ed25519 keypair for release signing.
//
// Usage:
//
//	go run scripts/generate-signing-key.go
//
// Output:
//   - Prints the base64-encoded public key (embed in signing.go)
//   - Prints the base64-encoded private key (store as RELEASE_SIGNING_KEY secret)
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate key: %v\n", err)
		os.Exit(1)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	privB64 := base64.StdEncoding.EncodeToString(priv)

	fmt.Println("=== Release Signing Key ===")
	fmt.Println()
	fmt.Println("PUBLIC KEY (embed in internal/updater/signing.go):")
	fmt.Println(pubB64)
	fmt.Println()
	fmt.Println("PRIVATE KEY (store as RELEASE_SIGNING_KEY GitHub Actions secret):")
	fmt.Println(privB64)
	fmt.Println()
	fmt.Println("Steps:")
	fmt.Println("1. Replace releasePublicKey in internal/updater/signing.go with the public key above")
	fmt.Println("2. Add the private key as a GitHub Actions secret:")
	fmt.Println()
	fmt.Printf("   echo '%s' | gh secret set RELEASE_SIGNING_KEY --repo carryon-dev/cli\n", privB64)
	fmt.Println()
	fmt.Println("3. The release workflow will automatically sign checksums.txt")
}
