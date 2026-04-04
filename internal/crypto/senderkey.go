package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// SenderKeyBundle is the encrypted package sent through the signaling channel.
type SenderKeyBundle struct {
	EncryptedData []byte            `json:"encrypted_data"` // AES-256-GCM encrypted payload
	Nonce         []byte            `json:"nonce"`          // GCM nonce for data encryption
	WrappedKeys   map[string][]byte `json:"wrapped_keys"`   // device_id -> encrypted data key
	KeyNonces     map[string][]byte `json:"key_nonces"`     // device_id -> GCM nonce for key wrap
}

const senderKeyWrapInfo = "carryon-sender-key-wrap"

// Fixed application-specific salt for HKDF. Using a non-nil salt is recommended
// per RFC 5869 to provide domain separation even when the input key material
// is already high-entropy.
var hkdfSalt = []byte("carryon-e2e-v1")

// deriveWrappingKey computes the X25519 shared secret from privKey and peerPub,
// then derives a 32-byte AES wrapping key via HKDF-SHA256.
func deriveWrappingKey(privKey, peerPub []byte) ([]byte, error) {
	shared, err := curve25519.X25519(privKey, peerPub)
	if err != nil {
		return nil, fmt.Errorf("X25519: %w", err)
	}

	hk := hkdf.New(sha256.New, shared, hkdfSalt, []byte(senderKeyWrapInfo))
	wrappingKey := make([]byte, 32)
	if _, err := io.ReadFull(hk, wrappingKey); err != nil {
		return nil, fmt.Errorf("hkdf expand: %w", err)
	}

	return wrappingKey, nil
}

// gcmEncrypt encrypts plaintext with AES-256-GCM using key.
// Returns ciphertext and the random nonce used.
func gcmEncrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// gcmDecrypt decrypts ciphertext with AES-256-GCM using key and nonce.
func gcmDecrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm.Open: %w", err)
	}

	return plaintext, nil
}

// SenderKeyEncrypt encrypts plaintext for a set of recipients using the sender-key model.
//
// A random AES-256-GCM data key is generated and used to encrypt plaintext once.
// The data key is then wrapped individually for each recipient using an X25519 shared
// secret between senderPriv and the recipient's public key, with the wrapping key
// derived via HKDF-SHA256.
//
// recipients maps device_id -> recipient X25519 public key (32 bytes).
// Returns JSON-marshaled SenderKeyBundle.
func SenderKeyEncrypt(plaintext []byte, senderPriv []byte, recipients map[string][]byte) ([]byte, error) {
	// Generate a random 32-byte data key.
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, fmt.Errorf("generating data key: %w", err)
	}

	// Encrypt plaintext with the data key.
	encryptedData, nonce, err := gcmEncrypt(dataKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting data: %w", err)
	}

	wrappedKeys := make(map[string][]byte, len(recipients))
	keyNonces := make(map[string][]byte, len(recipients))

	for deviceID, recipientPub := range recipients {
		wrappingKey, err := deriveWrappingKey(senderPriv, recipientPub)
		if err != nil {
			return nil, fmt.Errorf("deriving wrapping key for %s: %w", deviceID, err)
		}

		wrappedKey, keyNonce, err := gcmEncrypt(wrappingKey, dataKey)
		if err != nil {
			return nil, fmt.Errorf("wrapping key for %s: %w", deviceID, err)
		}

		wrappedKeys[deviceID] = wrappedKey
		keyNonces[deviceID] = keyNonce
	}

	bundle := SenderKeyBundle{
		EncryptedData: encryptedData,
		Nonce:         nonce,
		WrappedKeys:   wrappedKeys,
		KeyNonces:     keyNonces,
	}

	out, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshaling bundle: %w", err)
	}

	return out, nil
}

// SenderKeyDecrypt decrypts a SenderKeyBundle for a specific device.
//
// It finds the wrapped data key for deviceID, unwraps it using the shared secret
// between recipientPriv and senderPub, then uses the data key to decrypt the payload.
// Returns the original plaintext.
func SenderKeyDecrypt(bundleJSON []byte, deviceID string, recipientPriv []byte, senderPub []byte) ([]byte, error) {
	var bundle SenderKeyBundle
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		return nil, fmt.Errorf("unmarshaling bundle: %w", err)
	}

	wrappedKey, ok := bundle.WrappedKeys[deviceID]
	if !ok {
		return nil, fmt.Errorf("no wrapped key for device %q", deviceID)
	}

	keyNonce, ok := bundle.KeyNonces[deviceID]
	if !ok {
		return nil, fmt.Errorf("no key nonce for device %q", deviceID)
	}

	wrappingKey, err := deriveWrappingKey(recipientPriv, senderPub)
	if err != nil {
		return nil, fmt.Errorf("deriving wrapping key: %w", err)
	}

	dataKey, err := gcmDecrypt(wrappingKey, keyNonce, wrappedKey)
	if err != nil {
		return nil, fmt.Errorf("unwrapping data key: %w", err)
	}

	plaintext, err := gcmDecrypt(dataKey, bundle.Nonce, bundle.EncryptedData)
	if err != nil {
		return nil, fmt.Errorf("decrypting data: %w", err)
	}

	return plaintext, nil
}
