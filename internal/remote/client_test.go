package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestSignalingClientConnect creates a mock WebSocket server, connects a
// SignalingClient to it, verifies that device.auth is sent, and verifies
// that a registered handler receives incoming messages.
func TestSignalingClientConnect(t *testing.T) {
	// authReceived is set when the server receives device.auth.
	var authReceived DeviceAuthMsg
	authCh := make(chan DeviceAuthMsg, 1)

	// echoToken is used to send a message from the server to the client.
	serverSendCh := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("server accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()

		// Read the first message - should be device.auth.
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Logf("server read: %v", err)
			return
		}

		msg, err := ParseSignalMsg(data)
		if err != nil {
			t.Logf("server parse: %v", err)
			return
		}

		if msg.Type == "device.auth" {
			var auth DeviceAuthMsg
			if err := json.Unmarshal(msg.Payload, &auth); err != nil {
				t.Logf("server unmarshal auth: %v", err)
				return
			}
			authReceived = auth
			authCh <- auth
		}

		// Send a message to the client.
		if toSend, ok := <-serverSendCh; ok {
			if err := conn.Write(ctx, websocket.MessageText, toSend); err != nil {
				t.Logf("server write: %v", err)
			}
		}
	}))
	defer srv.Close()

	// Convert http:// URL to ws://.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	pubKey := []byte("test-public-key-32-bytes-padding!")
	client := NewSignalingClient(wsURL, "dev-abc", "Test Device", "team-1", "tok-xyz", pubKey)

	// Register a handler before connecting.
	receivedCh := make(chan json.RawMessage, 1)
	client.OnMessage("test.ping", func(payload json.RawMessage) {
		receivedCh <- payload
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	// Verify device.auth was received.
	select {
	case auth := <-authCh:
		if auth.DeviceID != "dev-abc" {
			t.Errorf("DeviceID: got %q, want %q", auth.DeviceID, "dev-abc")
		}
		_ = authReceived
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for device.auth")
	}

	// Send a test.ping message from the server.
	pingMsg, err := NewSignalMsg("test.ping", map[string]string{"msg": "hello"})
	if err != nil {
		t.Fatalf("NewSignalMsg: %v", err)
	}
	serverSendCh <- pingMsg

	// Verify the handler was called.
	select {
	case payload := <-receivedCh:
		var p map[string]string
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if p["msg"] != "hello" {
			t.Errorf("payload msg: got %q, want %q", p["msg"], "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler to be called")
	}
}

// TestSignalingClientDone verifies that the Done channel closes when the
// server closes the connection.
func TestSignalingClientDone(t *testing.T) {
	closeSrv := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		// Read device.auth then close immediately.
		ctx := r.Context()
		conn.Read(ctx) //nolint
		conn.Close(websocket.StatusNormalClosure, "goodbye")
		close(closeSrv)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := NewSignalingClient(wsURL, "dev-1", "Device One", "team-1", "tok-1", []byte("pubkey"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-client.Done():
		// Good - connection dropped as expected.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Done channel to close")
	}
}

// TestSignalingClientSend verifies that Send transmits a message the server
// can read back.
func TestSignalingClientSend(t *testing.T) {
	receivedCh := make(chan SignalMsg, 2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		ctx := r.Context()

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			msg, err := ParseSignalMsg(data)
			if err != nil {
				continue
			}
			receivedCh <- msg
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := NewSignalingClient(wsURL, "dev-2", "Device Two", "team-1", "tok-2", []byte("pubkey"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	// First message is device.auth; drain it.
	select {
	case <-receivedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for device.auth")
	}

	// Now send a custom message.
	if err := client.Send(ctx, "sessions.update", map[string]string{"blob": "abc"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-receivedCh:
		if msg.Type != "sessions.update" {
			t.Errorf("type: got %q, want %q", msg.Type, "sessions.update")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sent message")
	}
}
