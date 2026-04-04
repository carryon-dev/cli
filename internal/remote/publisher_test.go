package remote

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/crypto"
)

func TestBuildSessionBlob(t *testing.T) {
	senderPub, senderPriv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("generate sender keypair: %v", err)
	}

	recipientPub, recipientPriv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("generate recipient keypair: %v", err)
	}

	sessions := []backend.Session{
		{ID: "native-1", Name: "my-project", Backend: "native", Created: 1000},
		{ID: "native-2", Name: "server", Backend: "native", Created: 2000, LastAttached: 2500},
	}

	recipients := map[string][]byte{
		"dev-recipient": recipientPub,
	}

	blob, err := BuildSessionBlob(sessions, "dev-sender", "Sender Laptop", senderPriv, recipients)
	if err != nil {
		t.Fatalf("BuildSessionBlob: %v", err)
	}

	// Verify recipient can decrypt
	bundleJSON, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	plaintext, err := crypto.SenderKeyDecrypt(bundleJSON, "dev-recipient", recipientPriv, senderPub)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	var envelope sessionBlobEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Timestamp == 0 {
		t.Fatal("expected non-zero timestamp in envelope")
	}

	decoded := envelope.Sessions
	if len(decoded) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(decoded))
	}
	if decoded[0].Name != "my-project" {
		t.Fatalf("expected my-project, got %s", decoded[0].Name)
	}
	if decoded[0].DeviceID != "dev-sender" {
		t.Fatalf("expected dev-sender, got %s", decoded[0].DeviceID)
	}
}

func TestBuildSessionBlobEmptyRecipients(t *testing.T) {
	_, senderPriv, _ := crypto.GenerateKeypair()
	sessions := []backend.Session{{ID: "s1", Name: "test"}}

	blob, err := BuildSessionBlob(sessions, "dev-1", "Laptop", senderPriv, map[string][]byte{})
	if err != nil {
		t.Fatalf("should succeed with empty recipients: %v", err)
	}
	if blob != "" {
		t.Fatal("expected empty blob for no recipients")
	}
}

func TestDecryptSessionBlob(t *testing.T) {
	senderPub, senderPriv, _ := crypto.GenerateKeypair()
	recipientPub, recipientPriv, _ := crypto.GenerateKeypair()

	sessions := []backend.Session{
		{ID: "s1", Name: "test-session", Backend: "native", Created: 3000},
	}

	blob, err := BuildSessionBlob(sessions, "dev-sender", "Sender", senderPriv, map[string][]byte{
		"dev-recipient": recipientPub,
	})
	if err != nil {
		t.Fatalf("build blob: %v", err)
	}

	decoded, blobTS, err := DecryptSessionBlob(blob, "dev-recipient", recipientPriv, senderPub)
	if err != nil {
		t.Fatalf("decrypt blob: %v", err)
	}
	if blobTS == 0 {
		t.Fatal("expected non-zero blob timestamp")
	}

	if len(decoded) != 1 {
		t.Fatalf("expected 1 session, got %d", len(decoded))
	}
	if decoded[0].Name != "test-session" {
		t.Fatalf("expected test-session, got %s", decoded[0].Name)
	}
}

func TestDecryptSessionBlobBadBlob(t *testing.T) {
	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	senderPub, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("generate sender keypair: %v", err)
	}

	// Garbage base64 blob should fail to decode.
	_, _, err = DecryptSessionBlob("not-valid-base64!!!", "dev-1", priv, senderPub)
	if err == nil {
		t.Fatal("expected error for invalid base64 blob")
	}

	// Valid base64 but not valid encrypted data should fail to decrypt.
	_, _, err = DecryptSessionBlob("aGVsbG8=", "dev-1", priv, senderPub)
	if err == nil {
		t.Fatal("expected error for non-encrypted blob")
	}
}
