package crypto

import (
	"bytes"
	"crypto/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// setupStreamPair creates two StreamCiphers that communicate with each other.
// Returns (initiatorCipher, responderCipher).
func setupStreamPair(t *testing.T) (*StreamCipher, *StreamCipher) {
	t.Helper()

	pubA, privA, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}

	pubB, privB, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	// A is initiator, B is responder.
	cipherA, err := NewStreamCipher(privA, pubB, true)
	if err != nil {
		t.Fatalf("NewStreamCipher A: %v", err)
	}

	cipherB, err := NewStreamCipher(privB, pubA, false)
	if err != nil {
		t.Fatalf("NewStreamCipher B: %v", err)
	}

	return cipherA, cipherB
}

// TestStreamEncryptRoundTrip verifies bidirectional encryption:
// A encrypts -> B decrypts, then B encrypts -> A decrypts.
func TestStreamEncryptRoundTrip(t *testing.T) {
	cipherA, cipherB := setupStreamPair(t)

	// A -> B
	msgAtoB := []byte("hello from A")
	encrypted, err := cipherA.Encrypt(msgAtoB)
	if err != nil {
		t.Fatalf("A Encrypt: %v", err)
	}

	decrypted, err := cipherB.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("B Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, msgAtoB) {
		t.Errorf("A->B round-trip mismatch: got %q, want %q", decrypted, msgAtoB)
	}

	// B -> A
	msgBtoA := []byte("hello from B")
	encrypted, err = cipherB.Encrypt(msgBtoA)
	if err != nil {
		t.Fatalf("B Encrypt: %v", err)
	}

	decrypted, err = cipherA.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("A Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, msgBtoA) {
		t.Errorf("B->A round-trip mismatch: got %q, want %q", decrypted, msgBtoA)
	}
}

// TestStreamEncryptTampered verifies that tampering with the ciphertext causes
// decryption to fail with an error (AEAD authentication failure).
func TestStreamEncryptTampered(t *testing.T) {
	cipherA, cipherB := setupStreamPair(t)

	msg := []byte("sensitive data")
	encrypted, err := cipherA.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a byte in the ciphertext portion (after the 8-byte counter prefix).
	tampered := make([]byte, len(encrypted))
	copy(tampered, encrypted)
	tampered[10] ^= 0xFF

	_, err = cipherB.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected decryption to fail after tampering, but it succeeded")
	}
}

// TestStreamEncryptLargePayload verifies that a 256 KB payload survives a
// round-trip without corruption.
func TestStreamEncryptLargePayload(t *testing.T) {
	cipherA, cipherB := setupStreamPair(t)

	const size = 256 * 1024
	plaintext := make([]byte, size)
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	encrypted, err := cipherA.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := cipherB.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("large payload round-trip mismatch")
	}
}

// TestStreamDecryptConcurrent verifies that concurrent Decrypt calls from
// multiple goroutines do not panic and that nonce tracking remains correct.
// Each goroutine encrypts a batch of frames and the receiver decrypts them;
// because Decrypt requires strictly increasing counters only one goroutine
// can decrypt at a time, so we verify the total success count matches the
// number of frames actually produced and that no replay error is silently lost.
func TestStreamDecryptConcurrent(t *testing.T) {
	const goroutines = 8
	const framesPerGoroutine = 20

	cipherA, cipherB := setupStreamPair(t)

	// Pre-encrypt all frames from A so we have stable counter values.
	total := goroutines * framesPerGoroutine
	encrypted := make([][]byte, total)
	for i := 0; i < total; i++ {
		msg := make([]byte, 32)
		if _, err := rand.Read(msg); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		enc, err := cipherA.Encrypt(msg)
		if err != nil {
			t.Fatalf("Encrypt %d: %v", i, err)
		}
		encrypted[i] = enc
	}

	// Decrypt all frames concurrently. Because nonces are strictly ordered,
	// exactly one goroutine wins each slot; the others get replay errors.
	// The important invariant is: no panic, and the total of successes +
	// expected-replay-errors equals total frames.
	var successCount atomic.Int64
	var replayCount atomic.Int64
	var unexpectedErrCount atomic.Int64

	var wg sync.WaitGroup
	// Feed frames across goroutines in round-robin order (each goroutine gets
	// every Nth frame) so counters are spread out and racing is realistic.
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := g; i < total; i += goroutines {
				_, err := cipherB.Decrypt(encrypted[i])
				if err == nil {
					successCount.Add(1)
				} else if isReplayError(err) {
					replayCount.Add(1)
				} else {
					unexpectedErrCount.Add(1)
					t.Errorf("unexpected error decrypting frame %d: %v", i, err)
				}
			}
		}()
	}
	wg.Wait()

	if unexpectedErrCount.Load() > 0 {
		t.Fatalf("%d unexpected decrypt errors", unexpectedErrCount.Load())
	}

	// successes + replays must account for every frame
	if got := successCount.Load() + replayCount.Load(); got != int64(total) {
		t.Errorf("success(%d) + replay(%d) = %d, want %d", successCount.Load(), replayCount.Load(), got, total)
	}

	// At least some frames must have succeeded (the first goroutine to reach
	// each slot wins; with 8 goroutines racing we expect close to total/goroutines
	// successes but the exact number is non-deterministic).
	if successCount.Load() == 0 {
		t.Error("expected at least one successful decrypt, got 0")
	}
}

// isReplayError returns true if err is the expected replay-detected error from
// StreamCipher.Decrypt.
func isReplayError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "replay detected")
}

// TestStreamEncryptConcurrent verifies that concurrent Encrypt calls increment
// the nonce atomically and produce unique, non-zero counter values.
func TestStreamEncryptConcurrent(t *testing.T) {
	const goroutines = 8
	const framesPerGoroutine = 50

	cipherA, _ := setupStreamPair(t)

	results := make(chan []byte, goroutines*framesPerGoroutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < framesPerGoroutine; i++ {
				enc, err := cipherA.Encrypt([]byte("hello"))
				if err != nil {
					t.Errorf("Encrypt: %v", err)
					return
				}
				results <- enc
			}
		}()
	}
	wg.Wait()
	close(results)

	// Collect all counter values and verify they are unique.
	seen := make(map[uint64]bool)
	for enc := range results {
		if len(enc) < 8 {
			t.Fatalf("encrypted frame too short: %d bytes", len(enc))
		}
		var counter uint64
		for i := 0; i < 8; i++ {
			counter = (counter << 8) | uint64(enc[i])
		}
		if seen[counter] {
			t.Errorf("duplicate nonce counter %d", counter)
		}
		seen[counter] = true
	}

	total := goroutines * framesPerGoroutine
	if len(seen) != total {
		t.Errorf("expected %d unique counters, got %d", total, len(seen))
	}
}

// TestStreamEncryptReplayRejected verifies that replaying a previously seen
// frame is rejected (counter <= last seen counter).
func TestStreamEncryptReplayRejected(t *testing.T) {
	cipherA, cipherB := setupStreamPair(t)

	// Send two frames from A.
	encrypted1, _ := cipherA.Encrypt([]byte("frame 1"))
	encrypted2, _ := cipherA.Encrypt([]byte("frame 2"))

	// B decrypts both in order.
	if _, err := cipherB.Decrypt(encrypted1); err != nil {
		t.Fatalf("Decrypt frame 1: %v", err)
	}
	if _, err := cipherB.Decrypt(encrypted2); err != nil {
		t.Fatalf("Decrypt frame 2: %v", err)
	}

	// Replay frame 1 - should be rejected.
	_, err := cipherB.Decrypt(encrypted1)
	if err == nil {
		t.Fatal("expected replay to be rejected, but decrypt succeeded")
	}

	// Replay frame 2 - should also be rejected.
	_, err = cipherB.Decrypt(encrypted2)
	if err == nil {
		t.Fatal("expected replay to be rejected, but decrypt succeeded")
	}
}
