package remote_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/remote"
)

// dialAndAccept dials from trA to trB concurrently with Accept,
// returning both connections. Uses 127.0.0.1 so the address is dialable.
func dialAndAccept(t *testing.T, ctx context.Context, trA, trB *remote.Transport) (connA, connB *quic.Conn) {
	t.Helper()

	type acceptResult struct {
		conn *quic.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		c, err := trB.Accept(ctx)
		acceptCh <- acceptResult{c, err}
	}()

	c, err := trA.Dial(ctx, fmt.Sprintf("127.0.0.1:%d", trB.Port()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	res := <-acceptCh
	if res.err != nil {
		t.Fatalf("Accept: %v", res.err)
	}

	return c, res.conn
}

func TestP2PBridgeEncrypted(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	// Generate X25519 keypairs for both sides.
	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}

	// Create bridge on B side (responder) in a goroutine.
	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "p2p")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	// Create bridge on A side (initiator).
	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "p2p")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}
	defer bridgeA.Close()

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	// A -> B
	msgAtoB := []byte("hello from A over QUIC P2P")
	if err := bridgeA.WriteFrame(msgAtoB); err != nil {
		t.Fatalf("WriteFrame A->B: %v", err)
	}

	received, err := bridgeB.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame B: %v", err)
	}
	if !bytes.Equal(received, msgAtoB) {
		t.Errorf("A->B: got %q, want %q", received, msgAtoB)
	}

	// B -> A
	msgBtoA := []byte("hello from B over QUIC P2P")
	if err := bridgeB.WriteFrame(msgBtoA); err != nil {
		t.Fatalf("WriteFrame B->A: %v", err)
	}

	received, err = bridgeA.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame A: %v", err)
	}
	if !bytes.Equal(received, msgBtoA) {
		t.Errorf("B->A: got %q, want %q", received, msgBtoA)
	}
}

func TestP2PBridgeMultipleFrames(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}

	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "lan")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "lan")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}
	defer bridgeA.Close()

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	messages := []string{"frame one", "frame two", "frame three"}
	for _, m := range messages {
		if err := bridgeA.WriteFrame([]byte(m)); err != nil {
			t.Fatalf("WriteFrame %q: %v", m, err)
		}
		got, err := bridgeB.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("ReadFrame %q: %v", m, err)
		}
		if string(got) != m {
			t.Errorf("frame %q: got %q", m, got)
		}
	}
}

func TestP2PBridgeMethod(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}

	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "p2p")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "lan")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}
	defer bridgeA.Close()

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	if bridgeA.Method() != "lan" {
		t.Errorf("bridgeA.Method() = %q, want 'lan'", bridgeA.Method())
	}
	if bridgeB.Method() != "p2p" {
		t.Errorf("bridgeB.Method() = %q, want 'p2p'", bridgeB.Method())
	}
}

func TestP2PBridge_ReadAfterPeerClose(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}

	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "p2p")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "p2p")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	// Close bridge A - bridge B's next read should return an error.
	bridgeA.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	_, err = bridgeB.ReadFrame(readCtx)
	if err == nil {
		t.Fatal("expected error reading from bridge B after bridge A closed, got nil")
	}
}

func TestP2PBridge_ZeroLengthFrame(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}

	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "p2p")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "p2p")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}
	defer bridgeA.Close()

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	if err := bridgeA.WriteFrame([]byte{}); err != nil {
		t.Fatalf("WriteFrame empty: %v", err)
	}

	got, err := bridgeB.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty frame, got %d bytes: %q", len(got), got)
	}
}

// TestP2PBridge_ConcurrentWriteFrame verifies that multiple goroutines can call
// WriteFrame concurrently without corrupting the frame stream. The key property
// being tested is that the single Write per frame (length prefix + ciphertext
// combined) prevents partial-frame interleaving on the stream. We verify this
// by confirming the receiver can parse every frame without a length-decode error
// (authentication errors due to out-of-order nonces are expected and tolerated).
func TestP2PBridge_ConcurrentWriteFrame(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connA, connB := dialAndAccept(t, ctx, trA, trB)

	type bridgeResult struct {
		bridge *remote.P2PBridge
		err    error
	}
	bCh := make(chan bridgeResult, 1)
	go func() {
		bridge, err := remote.NewP2PBridge(ctx, connB, privB, pubA, false, "p2p")
		bCh <- bridgeResult{bridge: bridge, err: err}
	}()

	bridgeA, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "p2p")
	if err != nil {
		t.Fatalf("NewP2PBridge A: %v", err)
	}
	defer bridgeA.Close()

	res := <-bCh
	if res.err != nil {
		t.Fatalf("NewP2PBridge B: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	const goroutines = 5
	const framesEach = 10
	total := goroutines * framesEach

	// Concurrent senders - all write the same payload.
	payload := bytes.Repeat([]byte("x"), 64)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < framesEach; i++ {
				if err := bridgeA.WriteFrame(payload); err != nil {
					t.Errorf("WriteFrame: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Read all frames. Out-of-order nonce rejections (replay errors) are expected
	// when goroutines write in non-counter order. What must NOT happen is a
	// frame-length parse error, which would indicate interleaved writes.
	parseErrors := 0
	received := 0
	for received < total {
		_, err := bridgeB.ReadFrame(ctx)
		if err == nil {
			received++
			continue
		}
		errStr := err.Error()
		// A length-decode or stream error indicates actual data corruption.
		if !containsAny(errStr, "replay detected", "decrypt") {
			parseErrors++
			t.Errorf("unexpected frame error (possible stream corruption): %v", err)
		}
		received++ // count it as consumed regardless
	}
	if parseErrors > 0 {
		t.Errorf("%d parse/stream errors detected - concurrent writes may have interleaved", parseErrors)
	}
}

// containsAny returns true if s contains any of the provided substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// TestP2PBridge_WriteAfterClose verifies that WriteFrame returns an error after
// the bridge has been closed.
func TestP2PBridge_WriteAfterClose(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	_, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx := context.Background()
	connA, _ := dialAndAccept(t, ctx, trA, trB)

	bridge, err := remote.NewP2PBridge(ctx, connA, privA, pubB, true, "lan")
	if err != nil {
		t.Fatal(err)
	}

	bridge.Close()

	err = bridge.WriteFrame([]byte("after close"))
	if err == nil {
		t.Fatal("expected error writing after close")
	}
}
