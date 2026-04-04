package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/coder/websocket"
)

// SignalingClient manages the WebSocket connection to the account Durable Object.
type SignalingClient struct {
	url          string
	deviceID     string
	deviceName   string
	teamID       string
	sessionToken string
	publicKey    []byte
	conn         *websocket.Conn
	mu           sync.Mutex
	handlers     map[string]func(json.RawMessage)
	done         chan struct{}
	closeErr     error // set by readLoop before closing done
	cancel       context.CancelFunc
}

// NewSignalingClient creates a new SignalingClient.
func NewSignalingClient(rawURL, deviceID, deviceName, teamID, sessionToken string, publicKey []byte) *SignalingClient {
	return &SignalingClient{
		url:          rawURL,
		deviceID:     deviceID,
		deviceName:   deviceName,
		teamID:       teamID,
		sessionToken: sessionToken,
		publicKey:    publicKey,
		handlers:     make(map[string]func(json.RawMessage)),
		done:         make(chan struct{}),
	}
}

// Connect dials the WebSocket, sends device.auth, and starts the read loop.
func (sc *SignalingClient) Connect(ctx context.Context) error {
	u, err := url.Parse(sc.url)
	if err != nil {
		return fmt.Errorf("parse signaling URL: %w", err)
	}

	// device_id and team_id are required by the signaling worker for
	// team membership verification and DO routing.
	q := u.Query()
	q.Set("device_id", sc.deviceID)
	q.Set("team_id", sc.teamID)
	u.RawQuery = q.Encode()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+sc.sessionToken)

	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}

	// Increase read limit from default 32KB - initial.state can be large
	// when the team has many devices with cached session blobs.
	conn.SetReadLimit(1 << 20) // 1 MiB

	readCtx, cancel := context.WithCancel(context.Background())

	sc.mu.Lock()
	sc.conn = conn
	sc.cancel = cancel
	sc.mu.Unlock()

	// Send device.auth message.
	authPayload := DeviceAuthMsg{
		DeviceID:  sc.deviceID,
		PublicKey: sc.publicKey,
	}
	if err := sc.Send(ctx, "device.auth", authPayload); err != nil {
		cancel()
		conn.Close(websocket.StatusInternalError, "auth failed")
		return fmt.Errorf("send device.auth: %w", err)
	}

	// Start background read loop.
	go sc.readLoop(readCtx)

	return nil
}

// readLoop reads messages from the WebSocket and dispatches to registered handlers.
// The context is cancelled by Close() to unblock the read.
func (sc *SignalingClient) readLoop(ctx context.Context) {
	defer close(sc.done)

	for {
		sc.mu.Lock()
		conn := sc.conn
		sc.mu.Unlock()
		if conn == nil {
			return
		}

		_, data, err := conn.Read(ctx)
		if err != nil {
			sc.mu.Lock()
			sc.closeErr = err
			sc.mu.Unlock()
			return
		}

		msg, err := ParseSignalMsg(data)
		if err != nil {
			continue
		}

		sc.mu.Lock()
		handler, ok := sc.handlers[msg.Type]
		sc.mu.Unlock()

		if ok {
			handler(msg.Payload)
		}
	}
}

// OnMessage registers a handler for the given message type.
// Overwrites any previously registered handler for the same type.
func (sc *SignalingClient) OnMessage(msgType string, handler func(json.RawMessage)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.handlers[msgType] = handler
}

// Send marshals payload into a SignalMsg and writes it to the WebSocket.
func (sc *SignalingClient) Send(ctx context.Context, msgType string, payload any) error {
	data, err := NewSignalMsg(msgType, payload)
	if err != nil {
		return fmt.Errorf("build signal message: %w", err)
	}

	sc.mu.Lock()
	conn := sc.conn
	sc.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("websocket write: %w", err)
	}

	return nil
}

// Close cancels the read loop context and closes the WebSocket connection.
func (sc *SignalingClient) Close() {
	sc.mu.Lock()
	conn := sc.conn
	cancel := sc.cancel
	sc.conn = nil
	sc.cancel = nil
	sc.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "closing")
	}
}

// Done returns a channel that is closed when the connection drops.
func (sc *SignalingClient) Done() <-chan struct{} {
	return sc.done
}

// CloseError returns the error that caused the connection to close, if any.
func (sc *SignalingClient) CloseError() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.closeErr
}
