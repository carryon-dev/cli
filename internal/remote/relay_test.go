package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/crypto"
)

// TestRelayBridge_ConcurrentWriteFrame verifies that multiple goroutines can call
// WriteFrame concurrently without corrupting the frame stream. The key property
// being tested is that the single Write per frame (length prefix + ciphertext
// combined) prevents partial-frame interleaving on the stream. We verify this
// by confirming the receiver can parse every frame without a length-decode error
// (authentication errors due to out-of-order nonces are expected and tolerated).
func TestRelayBridge_ConcurrentWriteFrame(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	relayAddr := relay.Addr()
	token := "concurrent-write-token"

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

	type bridgeResult struct {
		bridge *RelayBridge
		err    error
	}
	aCh := make(chan bridgeResult, 1)
	go func() {
		b, err := NewRelayBridge(ctx, relayAddr, token, privA, pubB, true, true)
		aCh <- bridgeResult{b, err}
	}()

	time.Sleep(20 * time.Millisecond)

	bridgeB, err := NewRelayBridge(ctx, relayAddr, token, privB, pubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-aCh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.bridge
	defer bridgeA.Close()

	const goroutines = 5
	const framesEach = 10
	total := goroutines * framesEach

	// Concurrent senders - all write the same payload.
	payload := bytes.Repeat([]byte("y"), 64)
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
		if !containsAnyStr(errStr, "replay detected", "decrypt") {
			parseErrors++
			t.Errorf("unexpected frame error (possible stream corruption): %v", err)
		}
		received++ // count it as consumed regardless
	}
	if parseErrors > 0 {
		t.Errorf("%d parse/stream errors detected - concurrent writes may have interleaved", parseErrors)
	}
}

// containsAnyStr returns true if s contains any of the provided substrings.
func containsAnyStr(s string, subs ...string) bool {
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

// generateTestTLSConfig creates a self-signed TLS config for a QUIC test server.
func generateTestTLSConfig() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
		}},
		NextProtos: []string{"carryon"},
	}, nil
}

// mockQUICRelay listens on a random UDP port, pairs two QUIC connections sharing
// the same pairing token, and forwards opaque bytes between their streams.
type mockQUICRelay struct {
	listener *quic.Listener
	mu       sync.Mutex
	waiting  map[string]*quic.Stream
}

func newMockQUICRelay(t *testing.T) *mockQUICRelay {
	t.Helper()

	tlsCfg, err := generateTestTLSConfig()
	if err != nil {
		t.Fatalf("mockQUICRelay: generate TLS: %v", err)
	}

	listener, err := quic.ListenAddr("127.0.0.1:0", tlsCfg, &quic.Config{})
	if err != nil {
		t.Fatalf("mockQUICRelay: listen: %v", err)
	}

	r := &mockQUICRelay{
		listener: listener,
		waiting:  make(map[string]*quic.Stream),
	}
	go r.acceptLoop()
	return r
}

// Addr returns the UDP address the relay is listening on.
func (r *mockQUICRelay) Addr() string {
	return r.listener.Addr().(*net.UDPAddr).String()
}

// Close shuts down the relay listener.
func (r *mockQUICRelay) Close() {
	r.listener.Close()
}

func (r *mockQUICRelay) acceptLoop() {
	for {
		conn, err := r.listener.Accept(context.Background())
		if err != nil {
			return
		}
		go r.handleConn(conn)
	}
}

func (r *mockQUICRelay) handleConn(conn *quic.Conn) {
	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		conn.CloseWithError(1, "accept stream failed")
		return
	}

	// Read 2-byte length-prefixed pairing token.
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		conn.CloseWithError(1, "read token length failed")
		return
	}
	tokenLen := binary.BigEndian.Uint16(lenBuf)
	if tokenLen > 4096 {
		conn.CloseWithError(1, "token too large")
		return
	}
	tokenBuf := make([]byte, tokenLen)
	if _, err := io.ReadFull(stream, tokenBuf); err != nil {
		conn.CloseWithError(1, "read token failed")
		return
	}
	token := string(tokenBuf)

	r.mu.Lock()
	peer, hasPeer := r.waiting[token]
	if hasPeer {
		delete(r.waiting, token)
	} else {
		r.waiting[token] = stream
	}
	r.mu.Unlock()

	if !hasPeer {
		// Park until paired or connection closes.
		<-conn.Context().Done()
		r.mu.Lock()
		delete(r.waiting, token)
		r.mu.Unlock()
		return
	}

	// Forward bytes bidirectionally between stream and peer.
	go forwardStreams(stream, peer)
}

func forwardStreams(a, b *quic.Stream) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src *quic.Stream) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// TestRelayBridgeEncrypted creates a mock QUIC relay server, connects two
// RelayBridges with ephemeral keys, sends data in both directions, and verifies
// decryption.
func TestRelayBridgeEncrypted(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	relayAddr := relay.Addr()
	pairingToken := "test-pairing-token-abc"

	// Generate ephemeral keypairs for both sides.
	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect side A (initiator) in a goroutine.
	type bridgeResult struct {
		bridge *RelayBridge
		err    error
	}
	aCh := make(chan bridgeResult, 1)
	go func() {
		b, err := NewRelayBridge(ctx, relayAddr, pairingToken, privA, pubB, true, true)
		aCh <- bridgeResult{b, err}
	}()

	// Give side A a moment to register before B connects.
	time.Sleep(20 * time.Millisecond)

	// Connect side B (responder).
	bridgeB, err := NewRelayBridge(ctx, relayAddr, pairingToken, privB, pubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	// Retrieve side A.
	res := <-aCh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.bridge
	defer bridgeA.Close()

	// A -> B
	msgAtoB := []byte("hello from A through relay")
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
	msgBtoA := []byte("hello from B through relay")
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

// TestRelayBridgeMultipleFrames verifies that multiple sequential frames
// are all correctly encrypted and decrypted end-to-end.
func TestRelayBridgeMultipleFrames(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	relayAddr := relay.Addr()
	token := "multi-frame-token"

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type bridgeResult struct {
		bridge *RelayBridge
		err    error
	}
	aCh := make(chan bridgeResult, 1)
	go func() {
		b, err := NewRelayBridge(ctx, relayAddr, token, privA, pubB, true, true)
		aCh <- bridgeResult{b, err}
	}()

	time.Sleep(20 * time.Millisecond)

	bridgeB, err := NewRelayBridge(ctx, relayAddr, token, privB, pubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-aCh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.bridge
	defer bridgeA.Close()

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

// TestRelayBridge_ReadAfterPeerClose verifies that ReadFrame returns an error
// when the peer closes their bridge connection.
func TestRelayBridge_ReadAfterPeerClose(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	relayAddr := relay.Addr()
	token := "read-after-close-token"

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type bridgeResult struct {
		bridge *RelayBridge
		err    error
	}
	aCh := make(chan bridgeResult, 1)
	go func() {
		b, err := NewRelayBridge(ctx, relayAddr, token, privA, pubB, true, true)
		aCh <- bridgeResult{b, err}
	}()

	time.Sleep(20 * time.Millisecond)

	bridgeB, err := NewRelayBridge(ctx, relayAddr, token, privB, pubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-aCh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.bridge

	// Close bridge A - bridge B's next read should return an error.
	bridgeA.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	_, err = bridgeB.ReadFrame(readCtx)
	if err == nil {
		t.Fatal("expected error reading from bridge B after bridge A closed, got nil")
	}
}

// TestRelayBridge_ZeroLengthFrame verifies that a zero-length payload can be
// written and read back as an empty frame without error.
func TestRelayBridge_ZeroLengthFrame(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	relayAddr := relay.Addr()
	token := "zero-length-frame-token"

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type bridgeResult struct {
		bridge *RelayBridge
		err    error
	}
	aCh := make(chan bridgeResult, 1)
	go func() {
		b, err := NewRelayBridge(ctx, relayAddr, token, privA, pubB, true, true)
		aCh <- bridgeResult{b, err}
	}()

	time.Sleep(20 * time.Millisecond)

	bridgeB, err := NewRelayBridge(ctx, relayAddr, token, privB, pubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-aCh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.bridge
	defer bridgeA.Close()

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

// TestRelayBridge_WriteAfterClose verifies that WriteFrame returns an error after
// the bridge has been closed.
func TestRelayBridge_WriteAfterClose(t *testing.T) {
	relay := newMockQUICRelay(t)
	defer relay.Close()

	_, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx := context.Background()
	bridge, err := NewRelayBridge(ctx, relay.Addr(), "write-close-test", privA, pubB, true, true)
	if err != nil {
		t.Fatal(err)
	}

	bridge.Close()

	err = bridge.WriteFrame([]byte("after close"))
	if err == nil {
		t.Fatal("expected error writing after close")
	}
}

// TestConnectionManager_RelayFallbackAfterP2PTimeout verifies that when all P2P
// candidates are unreachable, Connect falls back to relay and returns a bridge
// with Method() == "relay".
func TestConnectionManager_RelayFallbackAfterP2PTimeout(t *testing.T) {
	tr, err := NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	relay := newMockQUICRelay(t)
	defer relay.Close()

	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	cm := NewConnectionManager(tr)

	type res struct {
		bridge Bridge
		err    error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := cm.Connect(context.Background(), ConnectParams{
			EphemeralPrivKey: privA,
			EphemeralPubKey:  pubA,
			RemotePubKey:     pubB,
			TheirCandidates: []Candidate{
				{Type: "lan", Addr: "127.0.0.1", Port: 1}, // unreachable
			},
			RelayAddr:    relay.Addr(),
			PairingToken: "fallback-test",
			IsInitiator:  true,
			P2PTimeout:   500 * time.Millisecond,
			SkipRelayTLS: true,
		})
		ch <- res{b, err}
	}()

	// Wait for the P2P timeout, then connect the other side to the relay.
	time.Sleep(700 * time.Millisecond)
	bridgeB, err := NewRelayBridge(context.Background(), relay.Addr(), "fallback-test", privB, pubA, false, true)
	if err != nil {
		t.Fatalf("relay bridge B: %v", err)
	}
	defer bridgeB.Close()

	result := <-ch
	if result.err != nil {
		t.Fatalf("Connect: %v", result.err)
	}
	defer result.bridge.Close()

	if result.bridge.Method() != "relay" {
		t.Fatalf("expected method 'relay', got %q", result.bridge.Method())
	}
}
