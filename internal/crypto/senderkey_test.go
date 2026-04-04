package crypto

import (
	"bytes"
	"testing"
)

func TestSenderKeyRoundTrip(t *testing.T) {
	// Generate sender keypair.
	senderPub, senderPriv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() sender: %v", err)
	}

	// Generate two recipient keypairs.
	recip1Pub, recip1Priv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() recipient1: %v", err)
	}
	recip2Pub, recip2Priv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() recipient2: %v", err)
	}

	plaintext := []byte("hello from carryOn sender-key encryption")

	recipients := map[string][]byte{
		"device-1": recip1Pub,
		"device-2": recip2Pub,
	}

	bundleJSON, err := SenderKeyEncrypt(plaintext, senderPriv, recipients)
	if err != nil {
		t.Fatalf("SenderKeyEncrypt() error: %v", err)
	}

	// Recipient 1 should decrypt successfully.
	got1, err := SenderKeyDecrypt(bundleJSON, "device-1", recip1Priv, senderPub)
	if err != nil {
		t.Fatalf("SenderKeyDecrypt() device-1: %v", err)
	}
	if !bytes.Equal(got1, plaintext) {
		t.Errorf("device-1 plaintext = %q, want %q", got1, plaintext)
	}

	// Recipient 2 should decrypt successfully.
	got2, err := SenderKeyDecrypt(bundleJSON, "device-2", recip2Priv, senderPub)
	if err != nil {
		t.Fatalf("SenderKeyDecrypt() device-2: %v", err)
	}
	if !bytes.Equal(got2, plaintext) {
		t.Errorf("device-2 plaintext = %q, want %q", got2, plaintext)
	}
}

func TestSenderKeyWrongPrivateKey(t *testing.T) {
	// Use the real sender keypair so only the recipient key varies.
	senderPub, senderPriv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() sender: %v", err)
	}

	recip1Pub, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() recipient1: %v", err)
	}

	// Generate a different private key - not the one paired with recip1Pub.
	_, wrongPriv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() wrong private: %v", err)
	}

	recipients := map[string][]byte{
		"device-1": recip1Pub,
	}

	bundleJSON, err := SenderKeyEncrypt([]byte("secret data"), senderPriv, recipients)
	if err != nil {
		t.Fatalf("SenderKeyEncrypt() error: %v", err)
	}

	// Decrypt with correct sender pub but wrong recipient private key.
	// Fails because wrongPriv * senderPub != recip1Priv * senderPub.
	_, err = SenderKeyDecrypt(bundleJSON, "device-1", wrongPriv, senderPub)
	if err == nil {
		t.Error("SenderKeyDecrypt() with wrong private key: expected error, got nil")
	}
}

func TestSenderKeyUnknownDevice(t *testing.T) {
	senderPub, senderPriv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() sender: %v", err)
	}

	recip1Pub, recip1Priv, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() recipient1: %v", err)
	}

	recipients := map[string][]byte{
		"device-1": recip1Pub,
	}

	bundleJSON, err := SenderKeyEncrypt([]byte("secret data"), senderPriv, recipients)
	if err != nil {
		t.Fatalf("SenderKeyEncrypt() error: %v", err)
	}

	// Decrypt with a valid private key but an unknown device ID.
	_, err = SenderKeyDecrypt(bundleJSON, "device-unknown", recip1Priv, senderPub)
	if err == nil {
		t.Error("SenderKeyDecrypt() with unknown device ID: expected error, got nil")
	}
}
