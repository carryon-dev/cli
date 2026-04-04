package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/carryon-dev/cli/internal/backend"
)

// Client is the CLI-side IPC client that connects to the daemon socket,
// sends JSON-RPC requests, receives responses, and supports stream
// attachment and server-push notifications.
type Client struct {
	conn    net.Conn
	decoder *FrameDecoder
	mu      sync.Mutex

	pendingRequests       map[int]chan rpcResponse
	nextID                int
	streamListeners       map[string]map[int64]func([]byte) // sessionID -> listenerID -> callback
	nextListenerID        atomic.Int64
	notificationListeners map[string]func(map[string]any) // method -> callback
	onResizeRequest       func(string)                    // called with sessionID

	done chan struct{}
}

type rpcResponse struct {
	Result any
	Error  *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ClientStreamHandle provides read/write access to an attached session stream
// from the client side. It buffers frames received before OnData is called,
// so scrollback sent immediately on attach is not lost.
type ClientStreamHandle struct {
	client     *Client
	sessionID  string
	listenerID int64
	listeners  []func([]byte)
	buffered   [][]byte // frames received before OnData is called
	mu         sync.Mutex
}

// Write sends terminal data to the session via the IPC connection.
func (h *ClientStreamHandle) Write(data []byte) error {
	h.client.mu.Lock()
	conn := h.client.conn
	h.client.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	frame := EncodeFrame(Frame{
		Type:      backend.FrameTerminalData,
		SessionID: h.sessionID,
		Payload:   data,
	})
	_, err := conn.Write(frame)
	return err
}

// OnData registers a callback to receive terminal data for this session.
// Any frames buffered before this call (e.g. scrollback) are delivered immediately.
func (h *ClientStreamHandle) OnData(callback func([]byte)) {
	h.mu.Lock()
	h.listeners = append(h.listeners, callback)
	// Flush buffered frames to the new listener.
	buffered := h.buffered
	h.buffered = nil
	h.mu.Unlock()

	for _, data := range buffered {
		callback(data)
	}
}

// Close sends a StreamClose frame and removes only this handle's listener.
func (h *ClientStreamHandle) Close() {
	h.client.mu.Lock()
	conn := h.client.conn
	if m := h.client.streamListeners[h.sessionID]; m != nil {
		delete(m, h.listenerID)
		if len(m) == 0 {
			delete(h.client.streamListeners, h.sessionID)
		}
	}
	h.client.mu.Unlock()

	if conn != nil {
		frame := EncodeFrame(Frame{
			Type:      backend.FrameStreamClose,
			SessionID: h.sessionID,
			Payload:   nil,
		})
		conn.Write(frame)
	}

	h.mu.Lock()
	h.listeners = nil
	h.mu.Unlock()
}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		pendingRequests:       make(map[int]chan rpcResponse),
		nextID:                1,
		streamListeners:       make(map[string]map[int64]func([]byte)),
		notificationListeners: make(map[string]func(map[string]any)),
		done:                  make(chan struct{}),
	}
}

// Connect dials the Unix domain socket at socketPath and starts the
// internal read loop goroutine.
func (c *Client) Connect(socketPath string) error {
	conn, err := Dial(socketPath)
	if err != nil {
		return fmt.Errorf("connect %s: %w", socketPath, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.decoder = NewFrameDecoder()
	c.decoder.OnFrame(func(frame Frame) {
		c.handleFrame(frame)
	})
	c.mu.Unlock()

	go c.readLoop()
	return nil
}

// Disconnect closes the connection and rejects all pending requests.
func (c *Client) Disconnect() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	pending := c.pendingRequests
	c.pendingRequests = make(map[int]chan rpcResponse)
	c.mu.Unlock()

	// Reject all pending requests.
	for _, ch := range pending {
		ch <- rpcResponse{Error: &rpcError{Code: -1, Message: "Disconnected"}}
	}

	if conn != nil {
		conn.Close()
	}
}

// Call sends a JSON-RPC request and waits for the response.
func (c *Client) Call(method string, params map[string]any) (any, error) {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, errors.New("not connected")
	}

	id := c.nextID
	c.nextID++

	respCh := make(chan rpcResponse, 1)
	c.pendingRequests[id] = respCh
	c.mu.Unlock()

	// Build JSON-RPC request.
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      id,
	}
	if params != nil {
		reqBody["params"] = params
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	frame := EncodeFrame(Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "",
		Payload:   payload,
	})

	if _, err := conn.Write(frame); err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Wait for response.
	resp := <-respCh
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// AttachStream returns a ClientStreamHandle for reading/writing terminal
// data for the given session. The caller should have already called
// session.attach via Call() first so the server sets up its side.
func (c *Client) AttachStream(sessionID string) *ClientStreamHandle {
	lid := c.nextListenerID.Add(1)
	h := &ClientStreamHandle{
		client:     c,
		sessionID:  sessionID,
		listenerID: lid,
	}

	// Register a buffering listener immediately so frames arriving before
	// OnData (e.g. scrollback sent on attach) are not lost.
	bufferFn := func(data []byte) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.listeners) == 0 {
			// No real listeners yet - buffer the data.
			cp := make([]byte, len(data))
			copy(cp, data)
			h.buffered = append(h.buffered, cp)
		} else {
			// Listeners registered - dispatch directly.
			for _, l := range h.listeners {
				l(data)
			}
		}
	}

	c.mu.Lock()
	if c.streamListeners[sessionID] == nil {
		c.streamListeners[sessionID] = make(map[int64]func([]byte))
	}
	c.streamListeners[sessionID][lid] = bufferFn
	c.mu.Unlock()

	return h
}

// SendResize sends a Resize frame with the given terminal dimensions.
func (c *Client) SendResize(sessionID string, cols, rows int) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return
	}

	payload, err := json.Marshal(map[string]int{
		"cols": cols,
		"rows": rows,
	})
	if err != nil {
		return
	}

	frame := EncodeFrame(Frame{
		Type:      backend.FrameResize,
		SessionID: sessionID,
		Payload:   payload,
	})
	conn.Write(frame)
}

// OnNotification registers a handler for server-push notifications with
// the given method name.
func (c *Client) OnNotification(method string, callback func(map[string]any)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notificationListeners[method] = callback
}

// OnResizeRequest registers a callback invoked when the server requests
// the client's current terminal dimensions for a session.
func (c *Client) OnResizeRequest(fn func(sessionID string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResizeRequest = fn
}

// readLoop reads from the connection and feeds data into the FrameDecoder.
func (c *Client) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		c.mu.Lock()
		conn := c.conn
		decoder := c.decoder
		c.mu.Unlock()

		if conn == nil {
			return
		}

		n, err := conn.Read(buf)
		if n > 0 {
			decoder.Push(buf[:n])
		}
		if err != nil {
			// Connection closed or error - clean up pending requests.
			c.mu.Lock()
			pending := c.pendingRequests
			c.pendingRequests = make(map[int]chan rpcResponse)
			c.conn = nil
			c.mu.Unlock()

			for _, ch := range pending {
				ch <- rpcResponse{Error: &rpcError{Code: -1, Message: "Connection closed"}}
			}
			return
		}
	}
}

// handleFrame dispatches a decoded frame to the appropriate handler.
func (c *Client) handleFrame(frame Frame) {
	switch frame.Type {
	case backend.FrameJsonRpc:
		c.handleJsonRpc(frame.Payload)

	case backend.FrameTerminalData:
		c.mu.Lock()
		listeners := make([]func([]byte), 0, len(c.streamListeners[frame.SessionID]))
		for _, fn := range c.streamListeners[frame.SessionID] {
			listeners = append(listeners, fn)
		}
		c.mu.Unlock()

		for _, listener := range listeners {
			listener(frame.Payload)
		}

	case backend.FrameResizeRequest:
		c.mu.Lock()
		fn := c.onResizeRequest
		c.mu.Unlock()
		if fn != nil {
			fn(frame.SessionID)
		}
	}
}

// handleJsonRpc parses a JSON-RPC response or notification.
func (c *Client) handleJsonRpc(payload []byte) {
	var message struct {
		ID     any            `json:"id"`
		Result any            `json:"result"`
		Error  *rpcError      `json:"error"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return
	}

	// Response (has id).
	if message.ID != nil {
		// JSON numbers deserialize as float64.
		var id int
		switch v := message.ID.(type) {
		case float64:
			id = int(v)
		default:
			return
		}

		c.mu.Lock()
		ch, ok := c.pendingRequests[id]
		if ok {
			delete(c.pendingRequests, id)
		}
		c.mu.Unlock()

		if ok {
			if message.Error != nil {
				ch <- rpcResponse{Error: message.Error}
			} else {
				ch <- rpcResponse{Result: message.Result}
			}
		}
	}

	// Notification (has method, no id or id is null).
	if message.Method != "" {
		c.mu.Lock()
		listener := c.notificationListeners[message.Method]
		c.mu.Unlock()

		if listener != nil {
			listener(message.Params)
		}
	}
}
