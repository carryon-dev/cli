package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/remote"
)

// generateE2ETLSConfig creates a self-signed TLS config for the mock QUIC relay.
func generateE2ETLSConfig() (*tls.Config, error) {
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

// mockE2EQUICRelay pairs two QUIC connections by token and forwards bytes
// bidirectionally between their streams.
type mockE2EQUICRelay struct {
	listener *quic.Listener
	mu       sync.Mutex
	waiting  map[string]*quic.Stream
}

func newMockE2EQUICRelay(t *testing.T) *mockE2EQUICRelay {
	t.Helper()

	tlsCfg, err := generateE2ETLSConfig()
	if err != nil {
		t.Fatalf("mockE2EQUICRelay: generate TLS: %v", err)
	}

	listener, err := quic.ListenAddr("127.0.0.1:0", tlsCfg, &quic.Config{})
	if err != nil {
		t.Fatalf("mockE2EQUICRelay: listen: %v", err)
	}

	r := &mockE2EQUICRelay{
		listener: listener,
		waiting:  make(map[string]*quic.Stream),
	}
	go r.acceptLoop()
	return r
}

// Addr returns the host:port address the relay listens on.
func (r *mockE2EQUICRelay) Addr() string {
	return r.listener.Addr().(*net.UDPAddr).String()
}

// Close shuts down the relay.
func (r *mockE2EQUICRelay) Close() {
	r.listener.Close()
}

func (r *mockE2EQUICRelay) acceptLoop() {
	for {
		conn, err := r.listener.Accept(context.Background())
		if err != nil {
			return
		}
		go r.handleConn(conn)
	}
}

func (r *mockE2EQUICRelay) handleConn(conn *quic.Conn) {
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
		// Park until peer arrives or connection closes.
		<-conn.Context().Done()
		r.mu.Lock()
		delete(r.waiting, token)
		r.mu.Unlock()
		return
	}

	// Forward bytes bidirectionally.
	go e2eForwardStreams(stream, peer)
}

func e2eForwardStreams(a, b *quic.Stream) {
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

// mockSignalingHandler acts as a simplified signaling server (Durable Object).
// It tracks connected devices by device_id, forwards connect.offer to the
// target device, and routes connect.answer back to the requesting client.
type pendingConnect struct {
	requesterDeviceID string
	requestID         string
}

type mockSignalingHandler struct {
	mu       sync.Mutex
	conns    map[string]*websocket.Conn // device_id -> conn
	pending  map[string]pendingConnect  // connection_id -> pending info
	relayAddr string                    // host:port of the mock QUIC relay
}

func newMockSignalingHandler(relayAddr string) *mockSignalingHandler {
	return &mockSignalingHandler{
		conns:     make(map[string]*websocket.Conn),
		pending:   make(map[string]pendingConnect),
		relayAddr: relayAddr,
	}
}

func (h *mockSignalingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	ctx := r.Context()

	// Read the first message which must be device.auth to get the device_id.
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}
	msg, err := remote.ParseSignalMsg(data)
	if err != nil || msg.Type != "device.auth" {
		return
	}
	var authMsg struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(msg.Payload, &authMsg); err != nil || authMsg.DeviceID == "" {
		return
	}
	deviceID := authMsg.DeviceID

	h.mu.Lock()
	h.conns[deviceID] = conn
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.conns, deviceID)
		h.mu.Unlock()
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		msg, err := remote.ParseSignalMsg(data)
		if err != nil {
			continue
		}

		switch msg.Type {
		case "device.auth":
			// Already processed above; ignore duplicates.

		case "connect.request":
			var req remote.ConnectRequestMsg
			if err := json.Unmarshal(msg.Payload, &req); err != nil {
				continue
			}

			connID := "conn-" + deviceID + "-" + req.TargetDeviceID
			pairingToken := "token-" + connID

			// Track which device is requesting so we can route the answer back.
			h.mu.Lock()
			h.pending[connID] = pendingConnect{
				requesterDeviceID: deviceID,
				requestID:         req.RequestID,
			}
			targetConn, targetOK := h.conns[req.TargetDeviceID]
			h.mu.Unlock()

			if !targetOK {
				continue
			}

			// Send connect.offer to the target device.
			offer := remote.ConnectOfferMsg{
				ConnectionID:    connID,
				RelayURL:        h.relayAddr,
				PairingToken:    pairingToken,
				RequesterPubkey: req.EphemeralPubkey,
			}
			offerData, err := remote.NewSignalMsg("connect.offer", offer)
			if err != nil {
				continue
			}

			targetConn.Write(ctx, websocket.MessageText, offerData) //nolint:errcheck

		case "connect.answer":
			var ans remote.ConnectAnswerMsg
			if err := json.Unmarshal(msg.Payload, &ans); err != nil {
				continue
			}

			h.mu.Lock()
			pc, ok := h.pending[ans.ConnectionID]
			if ok {
				delete(h.pending, ans.ConnectionID)
			}
			requesterConn, requesterOK := h.conns[pc.requesterDeviceID]
			h.mu.Unlock()

			if !ok || !requesterOK {
				continue
			}

			pairingToken := "token-" + ans.ConnectionID

			// Forward connect.answer.response back to the requester.
			resp := remote.ConnectAnswerResponseMsg{
				ConnectionID:    ans.ConnectionID,
				RequestID:       pc.requestID,
				RelayURL:        h.relayAddr,
				PairingToken:    pairingToken,
				ResponderPubkey: ans.EphemeralPubkey,
			}
			respData, err := remote.NewSignalMsg("connect.answer", resp)
			if err != nil {
				continue
			}

			requesterConn.Write(ctx, websocket.MessageText, respData) //nolint:errcheck

		case "session.create.request":
			var req struct {
				RequestID      string `json:"requestId"`
				TargetDeviceID string `json:"targetDeviceId"`
				Name           string `json:"name"`
			}
			json.Unmarshal(msg.Payload, &req)

			h.mu.Lock()
			// Track origin for routing response back.
			h.pending[req.RequestID] = pendingConnect{requesterDeviceID: deviceID}
			targetConn, targetOK := h.conns[req.TargetDeviceID]
			h.mu.Unlock()

			if !targetOK {
				// Send error back.
				errResp, _ := remote.NewSignalMsg("session.create.response", map[string]any{
					"requestId": req.RequestID,
					"error":     "device offline",
				})
				conn.Write(ctx, websocket.MessageText, errResp) //nolint:errcheck
				continue
			}

			// Forward to target with fromDeviceId.
			forward, _ := remote.NewSignalMsg("session.create.request", map[string]any{
				"requestId":    req.RequestID,
				"fromDeviceId": deviceID,
				"name":         req.Name,
			})
			targetConn.Write(ctx, websocket.MessageText, forward) //nolint:errcheck

		case "session.create.response":
			var resp struct {
				RequestID      string `json:"requestId"`
				TargetDeviceID string `json:"targetDeviceId"`
				SessionID      string `json:"sessionId"`
				Error          string `json:"error"`
			}
			json.Unmarshal(msg.Payload, &resp)

			h.mu.Lock()
			pc, ok := h.pending[resp.RequestID]
			if ok {
				delete(h.pending, resp.RequestID)
			}
			requesterConn, requesterOK := h.conns[pc.requesterDeviceID]
			h.mu.Unlock()

			if ok && requesterOK {
				fwd, _ := remote.NewSignalMsg("session.create.response", map[string]any{
					"requestId": resp.RequestID,
					"sessionId": resp.SessionID,
					"error":     resp.Error,
				})
				requesterConn.Write(ctx, websocket.MessageText, fwd) //nolint:errcheck
			}
		}
	}
}

// TestRemoteAttachE2E exercises the full remote relay flow end-to-end:
// signaling, pairing via mock DO, and encrypted data transfer through relay.
func TestRemoteAttachE2E(t *testing.T) {
	// 1. Start mock QUIC relay server.
	quicRelay := newMockE2EQUICRelay(t)
	defer quicRelay.Close()
	relayAddr := quicRelay.Addr()

	// 2. Start mock signaling server configured with relay address.
	sigHandler := newMockSignalingHandler(relayAddr)
	sigSrv := httptest.NewServer(sigHandler)
	defer sigSrv.Close()
	sigWS := "ws" + strings.TrimPrefix(sigSrv.URL, "http")

	// 3. Generate device keypairs for device A (requester) and device B (host).
	pubDevA, privDevA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair device A: %v", err)
	}
	pubDevB, privDevB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair device B: %v", err)
	}
	_ = privDevA
	_ = privDevB

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 4. Connect both signaling clients.
	clientA := remote.NewSignalingClient(sigWS, "device-a", "Device A", "team-1", "token-a", pubDevA)
	if err := clientA.Connect(ctx); err != nil {
		t.Fatalf("clientA.Connect: %v", err)
	}
	defer clientA.Close()

	clientB := remote.NewSignalingClient(sigWS, "device-b", "Device B", "team-1", "token-b", pubDevB)
	if err := clientB.Connect(ctx); err != nil {
		t.Fatalf("clientB.Connect: %v", err)
	}
	defer clientB.Close()

	// Give the server a moment to register both devices before we proceed.
	time.Sleep(20 * time.Millisecond)

	// 5. Generate ephemeral keypairs for this connection.
	ephPubA, ephPrivA, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair ephemeral A: %v", err)
	}
	ephPubB, ephPrivB, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair ephemeral B: %v", err)
	}

	// answerCh receives the connect.answer payload sent to client A.
	answerCh := make(chan remote.ConnectAnswerResponseMsg, 1)

	// 6. Register handlers.
	// Client B: handle connect.offer -> send connect.answer.
	clientB.OnMessage("connect.offer", func(payload json.RawMessage) {
		var offer remote.ConnectOfferMsg
		if err := json.Unmarshal(payload, &offer); err != nil {
			t.Logf("clientB unmarshal connect.offer: %v", err)
			return
		}

		ans := remote.ConnectAnswerMsg{
			ConnectionID:    offer.ConnectionID,
			EphemeralPubkey: ephPubB,
		}
		if err := clientB.Send(ctx, "connect.answer", ans); err != nil {
			t.Logf("clientB send connect.answer: %v", err)
		}
	})

	// Client A: handle connect.answer -> capture relay info.
	clientA.OnMessage("connect.answer", func(payload json.RawMessage) {
		var resp remote.ConnectAnswerResponseMsg
		if err := json.Unmarshal(payload, &resp); err != nil {
			t.Logf("clientA unmarshal connect.answer: %v", err)
			return
		}
		answerCh <- resp
	})

	// 7. Client A sends connect.request targeting device B.
	req := remote.ConnectRequestMsg{
		TargetDeviceID:  "device-b",
		TargetSessionID: "session-1",
		EphemeralPubkey: ephPubA,
	}
	if err := clientA.Send(ctx, "connect.request", req); err != nil {
		t.Fatalf("clientA.Send connect.request: %v", err)
	}

	// 8. Wait for connect.answer on Client A.
	var answer remote.ConnectAnswerResponseMsg
	select {
	case answer = <-answerCh:
		t.Logf("received connect.answer: connectionID=%s relayAddr=%s pairingToken=%s",
			answer.ConnectionID, answer.RelayURL, answer.PairingToken)
	case <-ctx.Done():
		t.Fatal("timed out waiting for connect.answer")
	}

	// Verify answer fields are populated.
	if answer.RelayURL == "" {
		t.Fatal("connect.answer missing relay_url")
	}
	if answer.PairingToken == "" {
		t.Fatal("connect.answer missing pairing_token")
	}
	if len(answer.ResponderPubkey) == 0 {
		t.Fatal("connect.answer missing responder_pubkey")
	}

	// 9. Both sides create RelayBridge using the pairing token and ephemeral keys.
	// A is the initiator; B is the responder.
	// Both sides connect concurrently since the relay parks the first connection.

	type bridgeOrErr struct {
		b   *remote.RelayBridge
		err error
	}
	bridgeACh := make(chan bridgeOrErr, 1)
	go func() {
		b, err := remote.NewRelayBridge(ctx, answer.RelayURL, answer.PairingToken, ephPrivA, ephPubB, true, true)
		bridgeACh <- bridgeOrErr{b, err}
	}()

	// Small delay so A registers with the relay before B connects.
	time.Sleep(20 * time.Millisecond)

	bridgeB, err := remote.NewRelayBridge(ctx, answer.RelayURL, answer.PairingToken, ephPrivB, ephPubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-bridgeACh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.b
	defer bridgeA.Close()

	// 10. Send data B->A, verify A receives correct plaintext.
	msgBtoA := []byte("hello from B to A through relay")
	if err := bridgeB.WriteFrame(msgBtoA); err != nil {
		t.Fatalf("WriteFrame B->A: %v", err)
	}
	gotBtoA, err := bridgeA.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame A (B->A): %v", err)
	}
	if !bytes.Equal(gotBtoA, msgBtoA) {
		t.Errorf("B->A: got %q, want %q", gotBtoA, msgBtoA)
	}

	// 11. Send data A->B, verify B receives correct plaintext.
	msgAtoB := []byte("hello from A to B through relay")
	if err := bridgeA.WriteFrame(msgAtoB); err != nil {
		t.Fatalf("WriteFrame A->B: %v", err)
	}
	gotAtoB, err := bridgeB.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame B (A->B): %v", err)
	}
	if !bytes.Equal(gotAtoB, msgAtoB) {
		t.Errorf("A->B: got %q, want %q", gotAtoB, msgAtoB)
	}

	// 12. Success.
	t.Log("TestRemoteAttachE2E passed - full remote relay flow verified")
}

// TestRemoteCreateE2E exercises the full session create + connect + relay flow:
// Device A requests session creation on Device B, then connects to it via relay.
func TestRemoteCreateE2E(t *testing.T) {
	// 1. Start mock QUIC relay server.
	quicRelay := newMockE2EQUICRelay(t)
	defer quicRelay.Close()
	relayAddr := quicRelay.Addr()

	// 2. Start mock signaling server.
	sigHandler := newMockSignalingHandler(relayAddr)
	sigSrv := httptest.NewServer(sigHandler)
	defer sigSrv.Close()
	sigWS := "ws" + strings.TrimPrefix(sigSrv.URL, "http")

	// 3. Generate device keypairs.
	pubDevA, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair A: %v", err)
	}
	pubDevB, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 4. Connect both signaling clients.
	clientA := remote.NewSignalingClient(sigWS, "device-a", "Device A", "team-1", "token-a", pubDevA)
	if err := clientA.Connect(ctx); err != nil {
		t.Fatalf("clientA.Connect: %v", err)
	}
	defer clientA.Close()

	clientB := remote.NewSignalingClient(sigWS, "device-b", "Device B", "team-1", "token-b", pubDevB)
	if err := clientB.Connect(ctx); err != nil {
		t.Fatalf("clientB.Connect: %v", err)
	}
	defer clientB.Close()

	time.Sleep(20 * time.Millisecond)

	// 5. Device A sends session.create.request targeting Device B.
	createResponseCh := make(chan map[string]any, 1)
	clientA.OnMessage("session.create.response", func(payload json.RawMessage) {
		var resp map[string]any
		json.Unmarshal(payload, &resp)
		createResponseCh <- resp
	})

	// Device B handles session.create.request by responding with a session ID.
	clientB.OnMessage("session.create.request", func(payload json.RawMessage) {
		var req map[string]any
		json.Unmarshal(payload, &req)

		// Simulate session creation - respond with a session ID.
		resp := map[string]any{
			"requestId":      req["requestId"],
			"targetDeviceId": "device-b",
			"sessionId":      "native-created-123",
		}
		clientB.Send(ctx, "session.create.response", resp) //nolint:errcheck
	})

	// Send the create request.
	requestID := "test-req-1"
	err = clientA.Send(ctx, "session.create.request", map[string]any{
		"requestId":      requestID,
		"targetDeviceId": "device-b",
		"name":           "test-remote-session",
	})
	if err != nil {
		t.Fatalf("send create request: %v", err)
	}

	// 6. Wait for create response on Device A.
	select {
	case resp := <-createResponseCh:
		sessionID, _ := resp["sessionId"].(string)
		if sessionID != "native-created-123" {
			t.Fatalf("expected session ID native-created-123, got %v", resp["sessionId"])
		}
		errMsg, _ := resp["error"].(string)
		if errMsg != "" {
			t.Fatalf("unexpected error: %s", errMsg)
		}
		t.Logf("Remote session created: %s", sessionID)
	case <-ctx.Done():
		t.Fatal("timed out waiting for session.create.response")
	}

	// 7. Now test the connect + relay flow (same as TestRemoteAttachE2E but after create).
	ephPubA, ephPrivA, _ := crypto.GenerateKeypair()
	ephPubB, ephPrivB, _ := crypto.GenerateKeypair()

	answerCh := make(chan remote.ConnectAnswerResponseMsg, 1)

	clientB.OnMessage("connect.offer", func(payload json.RawMessage) {
		var offer remote.ConnectOfferMsg
		json.Unmarshal(payload, &offer)

		ans := remote.ConnectAnswerMsg{
			ConnectionID:    offer.ConnectionID,
			EphemeralPubkey: ephPubB,
		}
		clientB.Send(ctx, "connect.answer", ans) //nolint:errcheck
	})

	clientA.OnMessage("connect.answer", func(payload json.RawMessage) {
		var resp remote.ConnectAnswerResponseMsg
		json.Unmarshal(payload, &resp)
		answerCh <- resp
	})

	// Device A requests connection to the newly created session.
	req := remote.ConnectRequestMsg{
		TargetDeviceID:  "device-b",
		TargetSessionID: "native-created-123",
		EphemeralPubkey: ephPubA,
	}
	if err := clientA.Send(ctx, "connect.request", req); err != nil {
		t.Fatalf("send connect.request: %v", err)
	}

	var answer remote.ConnectAnswerResponseMsg
	select {
	case answer = <-answerCh:
		t.Logf("connect.answer received: relay=%s token=%s", answer.RelayURL, answer.PairingToken)
	case <-ctx.Done():
		t.Fatal("timed out waiting for connect.answer")
	}

	if answer.RelayURL == "" {
		t.Fatal("missing relay URL")
	}
	if answer.PairingToken == "" {
		t.Fatal("missing pairing token")
	}

	// 8. Establish relay bridges.
	type bridgeOrErr struct {
		b   *remote.RelayBridge
		err error
	}
	bridgeACh := make(chan bridgeOrErr, 1)
	go func() {
		b, err := remote.NewRelayBridge(ctx, answer.RelayURL, answer.PairingToken, ephPrivA, ephPubB, true, true)
		bridgeACh <- bridgeOrErr{b, err}
	}()

	time.Sleep(20 * time.Millisecond)

	bridgeB, err := remote.NewRelayBridge(ctx, answer.RelayURL, answer.PairingToken, ephPrivB, ephPubA, false, true)
	if err != nil {
		t.Fatalf("NewRelayBridge B: %v", err)
	}
	defer bridgeB.Close()

	res := <-bridgeACh
	if res.err != nil {
		t.Fatalf("NewRelayBridge A: %v", res.err)
	}
	bridgeA := res.b
	defer bridgeA.Close()

	// 9. Verify bidirectional encrypted data transfer.
	msgBtoA := []byte("output from remote session on device B")
	if err := bridgeB.WriteFrame(msgBtoA); err != nil {
		t.Fatalf("WriteFrame B->A: %v", err)
	}
	gotBtoA, err := bridgeA.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame A: %v", err)
	}
	if !bytes.Equal(gotBtoA, msgBtoA) {
		t.Errorf("B->A mismatch: got %q, want %q", gotBtoA, msgBtoA)
	}

	msgAtoB := []byte("input from requester device A")
	if err := bridgeA.WriteFrame(msgAtoB); err != nil {
		t.Fatalf("WriteFrame A->B: %v", err)
	}
	gotAtoB, err := bridgeB.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame B: %v", err)
	}
	if !bytes.Equal(gotAtoB, msgAtoB) {
		t.Errorf("A->B mismatch: got %q, want %q", gotAtoB, msgAtoB)
	}

	t.Log("TestRemoteCreateE2E passed - session create + connect + relay verified")
}

// TestRemoteCreateOfflineDevice verifies that requesting session creation on
// an offline device returns an error response.
func TestRemoteCreateOfflineDevice(t *testing.T) {
	quicRelay := newMockE2EQUICRelay(t)
	defer quicRelay.Close()

	sigHandler := newMockSignalingHandler(quicRelay.Addr())
	sigSrv := httptest.NewServer(sigHandler)
	defer sigSrv.Close()
	sigWS := "ws" + strings.TrimPrefix(sigSrv.URL, "http")

	pubDevA, _, _ := crypto.GenerateKeypair()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientA := remote.NewSignalingClient(sigWS, "device-a", "Device A", "team-1", "token-a", pubDevA)
	if err := clientA.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer clientA.Close()

	time.Sleep(20 * time.Millisecond)

	responseCh := make(chan map[string]any, 1)
	clientA.OnMessage("session.create.response", func(payload json.RawMessage) {
		var resp map[string]any
		json.Unmarshal(payload, &resp)
		responseCh <- resp
	})

	// Request create on device-b which is NOT connected.
	err := clientA.Send(ctx, "session.create.request", map[string]any{
		"requestId":      "req-offline",
		"targetDeviceId": "device-b",
		"name":           "test",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case resp := <-responseCh:
		errMsg, _ := resp["error"].(string)
		if errMsg == "" {
			t.Fatal("expected error for offline device")
		}
		t.Logf("Got expected error: %s", errMsg)
	case <-ctx.Done():
		t.Fatal("timed out - expected error response for offline device")
	}
}

