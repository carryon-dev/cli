package crypto

import (
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const streamKeyInfo = "carryon-stream-keys"

// StreamCipher handles per-connection stream encryption using ChaCha20-Poly1305.
// Each direction uses a separate nonce counter to avoid nonce reuse.
type StreamCipher struct {
	sendKey   []byte
	recvKey   []byte
	sendAEAD  cipher.AEAD
	recvAEAD  cipher.AEAD
	sendNonce uint64
	recvNonce uint64
	mu        sync.Mutex
}

// NewStreamCipher creates a StreamCipher from an X25519 key exchange.
//
// myPriv and theirPub are 32-byte X25519 keys. The shared secret is computed
// via X25519, then two 32-byte directional keys are derived with HKDF-SHA256.
// If initiator is true the cipher uses keyA for sending and keyB for receiving;
// the non-initiator side uses the opposite assignment so both ends agree.
func NewStreamCipher(myPriv, theirPub []byte, initiator bool) (*StreamCipher, error) {
	shared, err := curve25519.X25519(myPriv, theirPub)
	if err != nil {
		return nil, fmt.Errorf("X25519: %w", err)
	}

	hk := hkdf.New(sha256.New, shared, hkdfSalt, []byte(streamKeyInfo))

	keyA := make([]byte, 32)
	if _, err := io.ReadFull(hk, keyA); err != nil {
		return nil, fmt.Errorf("hkdf keyA: %w", err)
	}

	keyB := make([]byte, 32)
	if _, err := io.ReadFull(hk, keyB); err != nil {
		return nil, fmt.Errorf("hkdf keyB: %w", err)
	}

	sc := &StreamCipher{}
	if initiator {
		sc.sendKey = keyA
		sc.recvKey = keyB
	} else {
		sc.sendKey = keyB
		sc.recvKey = keyA
	}

	sc.sendAEAD, err = chacha20poly1305.New(sc.sendKey)
	if err != nil {
		return nil, fmt.Errorf("chacha20poly1305.New sendKey: %w", err)
	}
	sc.recvAEAD, err = chacha20poly1305.New(sc.recvKey)
	if err != nil {
		return nil, fmt.Errorf("chacha20poly1305.New recvKey: %w", err)
	}

	return sc, nil
}

// Encrypt encrypts plaintext and returns an authenticated ciphertext prefixed
// with the 8-byte big-endian nonce counter. Thread-safe.
func (sc *StreamCipher) Encrypt(plaintext []byte) ([]byte, error) {
	out := make([]byte, 8+len(plaintext)+sc.sendAEAD.Overhead())
	n, err := sc.EncryptTo(out, plaintext)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

// EncryptTo encrypts plaintext into dst starting at offset 0 and returns the
// number of bytes written. dst must have at least EncryptedLen(len(plaintext))
// bytes of capacity. This avoids allocation when the caller provides a
// pre-allocated buffer (e.g. with space for a frame header before offset 0).
// Thread-safe.
func (sc *StreamCipher) EncryptTo(dst []byte, plaintext []byte) (int, error) {
	sc.mu.Lock()
	sc.sendNonce++
	counter := sc.sendNonce
	aead := sc.sendAEAD
	sc.mu.Unlock()

	needed := 8 + len(plaintext) + aead.Overhead()
	if len(dst) < needed {
		return 0, fmt.Errorf("dst too small: need %d, have %d", needed, len(dst))
	}

	// Write 8-byte counter.
	binary.BigEndian.PutUint64(dst[:8], counter)

	// Build 12-byte nonce: 4 zero bytes + 8-byte counter (big-endian).
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], counter)

	// Seal into dst[8:8] so the ciphertext lands right after the counter.
	aead.Seal(dst[8:8], nonce[:], plaintext, nil)

	return needed, nil
}

// EncryptedLen returns the total byte length of an encrypted frame
// (counter + ciphertext + AEAD overhead) for the given plaintext length.
func (sc *StreamCipher) EncryptedLen(plaintextLen int) int {
	return 8 + plaintextLen + sc.sendAEAD.Overhead()
}

// Decrypt authenticates and decrypts data produced by Encrypt. The first 8
// bytes are the nonce counter; the remainder is the authenticated ciphertext.
// Thread-safe.
func (sc *StreamCipher) Decrypt(data []byte) ([]byte, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("data too short: %d bytes", len(data))
	}

	counter := binary.BigEndian.Uint64(data[:8])
	ciphertext := data[8:]

	// Validate and reserve the counter atomically. We advance recvNonce
	// before decrypting so that a concurrent caller cannot pass the same
	// counter. If decryption fails the stream is desynchronized anyway -
	// no rollback is needed.
	sc.mu.Lock()
	if counter <= sc.recvNonce {
		sc.mu.Unlock()
		return nil, fmt.Errorf("replay detected: counter %d <= last seen %d", counter, sc.recvNonce)
	}
	sc.recvNonce = counter
	aead := sc.recvAEAD
	sc.mu.Unlock()

	// Reconstruct 12-byte nonce from counter.
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], counter)

	plaintext, err := aead.Open(nil, nonce[:], ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}
