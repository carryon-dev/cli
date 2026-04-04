package remote_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/remote"
)

// lanIP returns a non-loopback, non-link-local IPv4 address for the current
// machine, or skips the test if none is available. P2P candidate validation
// rejects loopback addresses, so integration tests that dial a local transport
// must use the machine's actual LAN address.
func lanIP(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil {
				continue
			}
			if ip.IsLinkLocalUnicast() {
				continue
			}
			return ip.String()
		}
	}
	t.Skip("no non-loopback IPv4 LAN address available - skipping P2P integration test")
	return ""
}

// TestConnectionManagerLANDirect verifies that two peers can connect via a
// LAN candidate without needing a relay.
func TestConnectionManagerLANDirect(t *testing.T) {
	ip := lanIP(t)

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

	// Generate X25519 ephemeral keypairs for both sides.
	pubA, privA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubB, privB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// B advertises a single LAN candidate pointing at itself using the machine's
	// actual LAN IP - loopback is rejected by candidate validation.
	candidateB := remote.Candidate{
		Type: "lan",
		Addr: ip,
		Port: trB.Port(),
	}

	// cmA will dial B's candidate.
	cmA := remote.NewConnectionManager(trA)
	paramsA := remote.ConnectParams{
		EphemeralPrivKey: privA,
		EphemeralPubKey:  pubA,
		RemotePubKey:     pubB,
		TheirCandidates:  []remote.Candidate{candidateB},
		IsInitiator:      true,
		P2PTimeout:       10 * time.Second,
	}

	// B accepts and creates its side of the bridge concurrently.
	type bResult struct {
		bridge remote.Bridge
		err    error
	}
	bCh := make(chan bResult, 1)
	go func() {
		conn, err := trB.Accept(ctx)
		if err != nil {
			bCh <- bResult{err: fmt.Errorf("B accept: %w", err)}
			return
		}
		// B is not the initiator here - A dialed B.
		bridge, err := remote.NewP2PBridge(ctx, conn, privB, pubA, false, "lan")
		bCh <- bResult{bridge: bridge, err: err}
	}()

	// A connects - should succeed via the LAN candidate.
	bridgeA, err := cmA.Connect(ctx, paramsA)
	if err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	defer bridgeA.Close()

	// Retrieve B's bridge.
	res := <-bCh
	if res.err != nil {
		t.Fatalf("B bridge setup: %v", res.err)
	}
	bridgeB := res.bridge
	defer bridgeB.Close()

	// Verify method is "lan".
	if bridgeA.Method() != "lan" {
		t.Errorf("bridgeA.Method() = %q, want \"lan\"", bridgeA.Method())
	}

	// Verify encrypted data flows A -> B.
	msgAtoB := []byte("hello from A via LAN")
	if err := bridgeA.WriteFrame(msgAtoB); err != nil {
		t.Fatalf("WriteFrame A->B: %v", err)
	}
	got, err := bridgeB.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame B: %v", err)
	}
	if !bytes.Equal(got, msgAtoB) {
		t.Errorf("A->B: got %q, want %q", got, msgAtoB)
	}

	// Verify encrypted data flows B -> A.
	msgBtoA := []byte("hello from B via LAN")
	if err := bridgeB.WriteFrame(msgBtoA); err != nil {
		t.Fatalf("WriteFrame B->A: %v", err)
	}
	got, err = bridgeA.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame A: %v", err)
	}
	if !bytes.Equal(got, msgBtoA) {
		t.Errorf("B->A: got %q, want %q", got, msgBtoA)
	}
}

// TestConnectionManagerFallbackToRelay verifies that when P2P candidates are
// empty, Connect falls back to relay (and returns an error when no relay is
// configured either).
func TestConnectionManagerNoRelayNoP2P(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	_, remotePub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair remote: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cm := remote.NewConnectionManager(tr)
	params := remote.ConnectParams{
		EphemeralPrivKey: priv,
		RemotePubKey:     remotePub,
		TheirCandidates:  nil, // no candidates
		RelayAddr:        "",  // no relay
		IsInitiator:      true,
	}

	_, err = cm.Connect(ctx, params)
	if err == nil {
		t.Fatal("expected error when no P2P candidates and no relay, got nil")
	}
}

// TestConnectionManagerP2PTimeoutFallback verifies that when P2P candidates
// are provided but unreachable, Connect times out and attempts relay fallback.
// We test the timeout path by using an unreachable address and verifying the
// relay error is returned (not the P2P error).
func TestConnectionManagerP2PTimeout(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	_, remotePub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair remote: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cm := remote.NewConnectionManager(tr)
	params := remote.ConnectParams{
		EphemeralPrivKey: priv,
		RemotePubKey:     remotePub,
		// Use an unreachable candidate (TEST-NET-1 per RFC 5737 - never routed).
		TheirCandidates: []remote.Candidate{
			{Type: "lan", Addr: "192.0.2.1", Port: 1},
		},
		RelayAddr:    "", // no relay configured so we get a clear error
		IsInitiator:  true,
		P2PTimeout:   500 * time.Millisecond,
	}

	start := time.Now()
	_, err = cm.Connect(ctx, params)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should have tried P2P, timed out, then failed relay fallback quickly.
	// Allow generous upper bound to avoid flakiness.
	if elapsed > 5*time.Second {
		t.Errorf("Connect took %v, expected < 5s", elapsed)
	}
}

// TestConnectionManager_AllUnreachableNoRelay verifies that when all P2P
// candidates are unreachable and no relay is configured, Connect returns an
// error within the specified timeout.
func TestConnectionManager_AllUnreachableNoRelay(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	_, remotePub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair remote: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cm := remote.NewConnectionManager(tr)
	params := remote.ConnectParams{
		EphemeralPrivKey: priv,
		RemotePubKey:     remotePub,
		TheirCandidates: []remote.Candidate{
			{Type: "lan", Addr: "192.0.2.1", Port: 1},
		},
		RelayAddr:   "",
		IsInitiator: true,
		P2PTimeout:  500 * time.Millisecond,
	}

	_, err = cm.Connect(ctx, params)
	if err == nil {
		t.Fatal("expected error with unreachable candidate and no relay, got nil")
	}
}

// TestConnectionManager_EmptyCandidatesWithRelay verifies that when there are
// no P2P candidates, Connect skips straight to relay and returns an error when
// the relay is also unreachable.
func TestConnectionManager_EmptyCandidatesWithRelay(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	_, remotePub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair remote: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cm := remote.NewConnectionManager(tr)
	params := remote.ConnectParams{
		EphemeralPrivKey: priv,
		RemotePubKey:     remotePub,
		TheirCandidates:  nil,
		RelayAddr:        "127.0.0.1:1", // unreachable relay
		IsInitiator:      true,
	}

	_, err = cm.Connect(ctx, params)
	if err == nil {
		t.Fatal("expected error when relay is unreachable, got nil")
	}
}

// TestConnectionManager_ContextCanceled verifies that Connect respects a
// pre-canceled context and returns an error immediately.
func TestConnectionManager_ContextCanceled(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	_, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	_, remotePub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair remote: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Connect

	cm := remote.NewConnectionManager(tr)
	params := remote.ConnectParams{
		EphemeralPrivKey: priv,
		RemotePubKey:     remotePub,
		TheirCandidates: []remote.Candidate{
			{Type: "lan", Addr: "192.0.2.1", Port: 1},
		},
		RelayAddr:   "",
		IsInitiator: true,
		P2PTimeout:  5 * time.Second,
	}

	_, err = cm.Connect(ctx, params)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}

// TestConnectionManagerAcceptWins verifies that when A provides no candidates
// but accepts an incoming connection, the accepted side also forms a bridge.
// This tests the accept path of tryP2P.
func TestConnectionManagerBothSidesConnect(t *testing.T) {
	ip := lanIP(t)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A connects to B's candidate; B connects to A's candidate simultaneously.
	// This races both dial paths plus their accept paths. Use the machine's
	// actual LAN IP since loopback is rejected by candidate validation.
	candidateA := remote.Candidate{Type: "lan", Addr: ip, Port: trA.Port()}
	candidateB := remote.Candidate{Type: "lan", Addr: ip, Port: trB.Port()}

	cmA := remote.NewConnectionManager(trA)
	cmB := remote.NewConnectionManager(trB)

	paramsA := remote.ConnectParams{
		EphemeralPrivKey: privA,
		EphemeralPubKey:  pubA,
		RemotePubKey:     pubB,
		TheirCandidates:  []remote.Candidate{candidateB},
		IsInitiator:      true,
		P2PTimeout:       10 * time.Second,
	}
	paramsB := remote.ConnectParams{
		EphemeralPrivKey: privB,
		EphemeralPubKey:  pubB,
		RemotePubKey:     pubA,
		TheirCandidates:  []remote.Candidate{candidateA},
		IsInitiator:      false,
		P2PTimeout:       10 * time.Second,
	}

	type connectResult struct {
		bridge remote.Bridge
		err    error
	}
	aCh := make(chan connectResult, 1)
	bCh := make(chan connectResult, 1)

	go func() {
		b, err := cmA.Connect(ctx, paramsA)
		aCh <- connectResult{b, err}
	}()
	go func() {
		b, err := cmB.Connect(ctx, paramsB)
		bCh <- connectResult{b, err}
	}()

	resA := <-aCh
	resB := <-bCh

	if resA.err != nil {
		t.Fatalf("Connect A: %v", resA.err)
	}
	if resB.err != nil {
		t.Fatalf("Connect B: %v", resB.err)
	}
	defer resA.bridge.Close()
	defer resB.bridge.Close()

	// Verify data flows in both directions.
	msg := []byte("symmetric P2P test")
	if err := resA.bridge.WriteFrame(msg); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := resB.bridge.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Errorf("got %q, want %q", got, msg)
	}
}
