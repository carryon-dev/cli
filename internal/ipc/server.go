package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/localhost"
	"github.com/carryon-dev/cli/internal/remote"
)


// ClientInfo holds the identity a client provides via client.identify.
type ClientInfo struct {
	Type string `json:"type"` // "cli", "vscode", "web", "relay"
	Name string `json:"name"` // display name, e.g. "VS Code", hostname
	PID  int    `json:"pid"`  // process ID (0 if not applicable, e.g. web)
}

// ClientState tracks a connected client's decoder, streams, and subscriptions.
type ClientState struct {
	ID              string
	Info            ClientInfo
	ConnectedAt     int64 // unix milliseconds
	decoder         *FrameDecoder
	conn            net.Conn
	directSessions     map[string]struct{} // locally-attached sessions (I/O via holder, not daemon)
	resizedSessions    map[string]bool     // sessionID -> true if client resized since last input
	subscriptions      map[string]func()   // unified cleanup map
	RemoteBridge    remote.Bridge // set when attached to remote session
	RemoteSessionID string              // session ID of remote attachment
	mu              sync.Mutex
}

// WriteIpcFrame writes a raw IPC frame to the client's connection. Thread-safe.
func (cs *ClientState) WriteIpcFrame(data []byte) error {
	cs.mu.Lock()
	conn := cs.conn
	cs.mu.Unlock()
	_, err := conn.Write(data)
	return err
}

// SetRemoteBridge sets or clears the remote bridge for this client. Thread-safe.
func (cs *ClientState) SetRemoteBridge(bridge remote.Bridge, sessionID string) {
	cs.mu.Lock()
	cs.RemoteBridge = bridge
	cs.RemoteSessionID = sessionID
	cs.mu.Unlock()
}

// GetRemoteBridge returns the current remote bridge and session ID. Thread-safe.
func (cs *ClientState) GetRemoteBridge() (remote.Bridge, string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.RemoteBridge, cs.RemoteSessionID
}

// Server is the IPC server that listens on a Unix domain socket,
// decodes frames, dispatches JSON-RPC methods, and manages client state.
type Server struct {
	listener        net.Listener
	socketPath      string
	context         *RpcContext
	clients         map[string]*ClientState
	methods         map[string]RpcHandler
	clientCount     int
	localhostServer *localhost.LocalhostServer
	mu              sync.Mutex
	done               chan struct{}
}

// NewServer creates a new IPC server for the given socket path and context.
func NewServer(socketPath string, ctx *RpcContext) *Server {
	s := &Server{
		socketPath: socketPath,
		context:    ctx,
		clients: make(map[string]*ClientState),
		methods: buildMethods(),
		done:    make(chan struct{}),
	}

	// Wire localhost service into the RPC context.
	ctx.Local = &localServiceAdapter{server: s}

	// Wire broadcast and client lookup into the RPC context.
	ctx.BroadcastFn = s.broadcastAll
	ctx.GetWebClients = func(sessionID string) []map[string]any {
		s.mu.Lock()
		srv := s.localhostServer
		s.mu.Unlock()
		if srv != nil {
			return srv.GetSessionClients(sessionID)
		}
		return nil
	}
	ctx.GetSessionClients = s.getSessionClients

	// Broadcast session.created to all connected clients.
	ctx.SessionManager.OnSessionCreated(func(sess backend.Session) {
		s.broadcastAll("session.created", map[string]any{
			"sessionId": sess.ID,
			"name":      sess.Name,
			"backend":   sess.Backend,
		})
	})

	// When a session ends, broadcast to ALL clients.
	ctx.SessionManager.OnSessionEnded(func(sessionID string) {
		s.broadcastAll("session.ended", map[string]any{
			"sessionId": sessionID,
		})
	})

	// Broadcast session.renamed to all connected clients.
	ctx.SessionManager.OnSessionRenamed(func(sessionID, name string) {
		s.broadcastAll("session.renamed", map[string]any{
			"sessionId": sessionID,
			"name":      name,
		})
	})

	return s
}

// Start begins listening on the socket and accepting connections.
func (s *Server) Start() error {
	// Clean up stale socket file.
	if _, err := os.Stat(s.socketPath); err == nil {
		os.Remove(s.socketPath)
	}

	// ListenSecure sets umask 0077 before creating the socket so it is
	// owner-only from the start, eliminating the race window between
	// socket creation and a subsequent chmod.
	ln, err := ListenSecure(s.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socketPath, err)
	}
	s.listener = ln

	go s.acceptLoop()
	return nil
}

// Stop closes all client connections, closes the listener, and removes the socket file.
func (s *Server) Stop() error {
	s.mu.Lock()
	// Collect detach info for broadcasting after releasing the lock.
	type detachInfo struct {
		clientID   string
		sessionIDs []string
	}
	var allDetached []detachInfo
	for _, client := range s.clients {
		detached := s.cleanupClient(client)
		if len(detached) > 0 {
			allDetached = append(allDetached, detachInfo{clientID: client.ID, sessionIDs: detached})
		}
	}
	s.clients = make(map[string]*ClientState)
	s.mu.Unlock()

	// Broadcast session.detached after releasing server mutex.
	for _, info := range allDetached {
		for _, sessionID := range info.sessionIDs {
			s.broadcastAll("session.detached", map[string]any{
				"sessionId": sessionID,
				"clientId":  info.clientID,
			})
		}
	}

	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}

	// Remove socket file.
	os.Remove(s.socketPath)
	return err
}

// GetClientCount returns the number of currently connected clients.
func (s *Server) GetClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed - stop accepting.
			return
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	s.mu.Lock()
	s.clientCount++
	clientID := fmt.Sprintf("client-%d", s.clientCount)
	decoder := NewFrameDecoder()
	client := &ClientState{
		ID:             clientID,
		ConnectedAt:    time.Now().UnixMilli(),
		decoder:        decoder,
		conn:           conn,
		directSessions:  make(map[string]struct{}),
		resizedSessions: make(map[string]bool),
		subscriptions:   make(map[string]func()),
	}
	s.clients[clientID] = client
	s.mu.Unlock()

	s.context.Logger.Debug("ipc", fmt.Sprintf("Client connected: %s", clientID))

	decoder.OnFrame(func(frame Frame) {
		s.handleFrame(client, frame)
	})

	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			decoder.Push(buf[:n])
		}
		if decoder.Err() {
			s.context.Logger.Warn("ipc", fmt.Sprintf("Client %s: malformed frame, closing connection", clientID))
			break
		}
		if err != nil {
			break
		}
	}

	s.mu.Lock()
	detached := s.cleanupClient(client)
	delete(s.clients, clientID)
	s.mu.Unlock()

	// Broadcast session.detached after releasing server mutex to avoid deadlock.
	for _, sessionID := range detached {
		s.broadcastAll("session.detached", map[string]any{
			"sessionId": sessionID,
			"clientId":  client.ID,
		})
	}

	s.context.Logger.Debug("ipc", fmt.Sprintf("Client disconnected: %s", clientID))
}

func (s *Server) handleFrame(client *ClientState, frame Frame) {
	switch frame.Type {
	case backend.FrameJsonRpc:
		s.handleRpc(client, frame.Payload)

	case backend.FrameTerminalData:
		// If this client hasn't resized since its last input for this
		// session, ask it to re-send dimensions. This handles focus
		// switches between CLI clients and web-to-CLI transitions.
		if frame.SessionID != "" {
			client.mu.Lock()
			resized := client.resizedSessions[frame.SessionID]
			client.resizedSessions[frame.SessionID] = false
			client.mu.Unlock()

			if !resized {
				resizeReqFrame := EncodeFrame(Frame{
					Type:      backend.FrameResizeRequest,
					SessionID: frame.SessionID,
					Payload:   nil,
				})
				client.WriteIpcFrame(resizeReqFrame)
			}
		}

		// Forward to remote bridge if attached.
		bridge, remoteSessionID := client.GetRemoteBridge()
		if bridge != nil && frame.SessionID == remoteSessionID {
			if err := bridge.WriteFrame(frame.Payload); err != nil {
				bridge.Close()
				client.SetRemoteBridge(nil, "")
			}
		}

	case backend.FrameResize:
		var dims struct {
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if err := json.Unmarshal(frame.Payload, &dims); err == nil {
			s.context.SessionManager.Resize(frame.SessionID, dims.Cols, dims.Rows)
			client.mu.Lock()
			client.resizedSessions[frame.SessionID] = true
			client.mu.Unlock()
		}
	}
}

func (s *Server) handleRpc(client *ClientState, payload []byte) {
	var request struct {
		JSONRPC string         `json:"jsonrpc"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
		ID      any            `json:"id"`
	}

	if err := json.Unmarshal(payload, &request); err != nil {
		s.sendRpc(client, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": -32700, "message": "Parse error"},
			"id":      nil,
		})
		return
	}

	handler, ok := s.methods[request.Method]
	if !ok {
		s.sendRpc(client, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": -32601, "message": fmt.Sprintf("Method not found: %s", request.Method)},
			"id":      request.ID,
		})
		return
	}

	params := request.Params
	if params == nil {
		params = make(map[string]any)
	}

	result, err := handler(params, s.context)
	if err != nil {
		s.sendRpc(client, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": -32603, "message": err.Error()},
			"id":      request.ID,
		})
		return
	}

	// Handle subscribe.cancel - server-side cleanup for the specific subscription only.
	// Release client.mu before calling unsub() to avoid deadlock with subscriber
	// callbacks that may write to this client.
	if request.Method == "subscribe.cancel" {
		if subID, ok := params["subscriptionId"].(string); ok {
			client.mu.Lock()
			unsub := client.subscriptions[subID]
			delete(client.subscriptions, subID)
			client.mu.Unlock()
			if unsub != nil {
				unsub()
			}
		}
	}

	s.sendRpc(client, map[string]any{
		"jsonrpc": "2.0",
		"result":  result.Value,
		"id":      request.ID,
	})

	// Execute PostRPC after response is sent.
	if result.PostRPC != nil {
		result.PostRPC(client)
	}
}

// broadcastAll sends a JSON-RPC notification to every connected client.
func (s *Server) broadcastAll(method string, params map[string]any) {
	s.mu.Lock()
	clients := make([]*ClientState, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, client := range clients {
		s.sendRpc(client, map[string]any{
			"jsonrpc": "2.0",
			"method":  method,
			"params":  params,
		})
	}
}

// getSessionClients returns info about all clients attached to a session.
func (s *Server) getSessionClients(sessionID string) []map[string]any {
	s.mu.Lock()
	clients := make([]*ClientState, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	var result []map[string]any
	for _, c := range clients {
		c.mu.Lock()
		_, direct := c.directSessions[sessionID]
		info := c.Info
		connectedAt := c.ConnectedAt
		c.mu.Unlock()
		if direct {
			result = append(result, map[string]any{
				"clientId":    c.ID,
				"type":        info.Type,
				"name":        info.Name,
				"pid":         info.PID,
				"connectedAt": connectedAt,
			})
		}
	}
	return result
}

func (s *Server) sendRpc(client *ClientState, message map[string]any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	frame := EncodeFrame(Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "",
		Payload:   payload,
	})
	client.mu.Lock()
	_, writeErr := client.conn.Write(frame)
	client.mu.Unlock()
	if writeErr != nil {
		go s.cleanupAndRemoveClient(client)
	}
}

// AutoStartLocal checks the config and starts the local server if enabled.
func (s *Server) AutoStartLocal() {
	if s.context.Config.GetBool("local.enabled") {
		if err := s.StartLocalhost(); err != nil {
			s.context.Logger.Error("local", fmt.Sprintf("Auto-start failed: %v", err))
		}
	}
}

// StartLocalhost creates and starts the localhost web server.
func (s *Server) StartLocalhost() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.localhostServer != nil {
		status := s.localhostServer.Status()
		if running, ok := status["running"].(bool); ok && running {
			return nil // Already running.
		}
	}

	port := s.context.Config.GetInt("local.port")
	expose := s.context.Config.GetBool("local.expose")

	s.localhostServer = localhost.NewLocalhostServer(
		s.context.SessionManager,
		s.context.Logger,
		port,
		expose,
		s.context.BaseDir,
	)

	// Load existing password hash from config
	if hash := s.context.Config.GetString("local.password"); hash != "" {
		s.localhostServer.SetPasswordHash(hash)
	}

	s.localhostServer.SetBroadcastFn(s.broadcastAll)

	// When a CLI client resizes the holder directly, the holder sends
	// FrameResizeRequest to the daemon connection. Forward this to the
	// localhost server so it clears its resize tracking, ensuring the
	// next web client input triggers a resize_request.
	if nb, ok := s.context.Registry.Get("native").(*backend.NativeBackend); ok {
		srv := s.localhostServer
		nb.OnResizeRequest(func(sessionID string) {
			srv.ClearLastResize(sessionID)
		})
	}

	return s.localhostServer.Start()
}

// StopLocalhost stops the localhost web server if running.
func (s *Server) StopLocalhost() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.localhostServer == nil {
		return nil
	}

	err := s.localhostServer.Stop()
	s.localhostServer = nil
	return err
}

// LocalhostStatus returns the status of the localhost server.
func (s *Server) LocalhostStatus() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.localhostServer == nil {
		return map[string]any{"running": false}
	}

	return s.localhostServer.Status()
}

// cleanupAndRemoveClient acquires the server mutex, cleans up the client, removes
// it from the client map, and broadcasts detach events. Safe to call from any goroutine.
func (s *Server) cleanupAndRemoveClient(client *ClientState) {
	s.mu.Lock()
	// Check if the client is still in the map - it may have already been cleaned up
	// by handleConnection or Stop.
	if _, ok := s.clients[client.ID]; !ok {
		s.mu.Unlock()
		return
	}
	detached := s.cleanupClient(client)
	delete(s.clients, client.ID)
	s.mu.Unlock()

	for _, sessionID := range detached {
		s.broadcastAll("session.detached", map[string]any{
			"sessionId": sessionID,
			"clientId":  client.ID,
		})
	}
}

// cleanupClient calls all subscription cleanups and closes the connection.
// Must be called with server mu held OR after the client is removed from the map.
// Returns the list of session IDs that were detached, so the caller can broadcast
// session.detached events after releasing the server mutex.
func (s *Server) cleanupClient(client *ClientState) []string {
	bridge, _ := client.GetRemoteBridge()
	if bridge != nil {
		bridge.Close()
		client.SetRemoteBridge(nil, "")
	}

	client.mu.Lock()
	// Collect detached session IDs before cleanup for broadcasting.
	detachedSessions := make([]string, 0, len(client.directSessions))
	for sessionID := range client.directSessions {
		detachedSessions = append(detachedSessions, sessionID)
	}
	client.directSessions = make(map[string]struct{})

	// Collect unsub funcs and release client.mu before calling them to avoid
	// deadlock: unsub() acquires SubscriptionManager.mu, while Dispatch holds
	// SubscriptionManager.mu.RLock and may block writing to this client.
	unsubs := make([]func(), 0, len(client.subscriptions))
	for _, unsub := range client.subscriptions {
		unsubs = append(unsubs, unsub)
	}
	client.subscriptions = make(map[string]func())
	client.mu.Unlock()

	for _, unsub := range unsubs {
		unsub()
	}

	client.conn.Close()

	return detachedSessions
}

// localServiceAdapter implements LocalService by wrapping the Server's localhost methods.
type localServiceAdapter struct {
	server *Server
}

func (a *localServiceAdapter) Start() error {
	return a.server.StartLocalhost()
}

func (a *localServiceAdapter) Stop() error {
	return a.server.StopLocalhost()
}

func (a *localServiceAdapter) Status() map[string]any {
	return a.server.LocalhostStatus()
}

func (a *localServiceAdapter) SetPassword(hash string) {
	a.server.mu.Lock()
	if a.server.localhostServer != nil {
		a.server.localhostServer.SetPasswordHash(hash)
	}
	a.server.mu.Unlock()
}

func (a *localServiceAdapter) KickWebClients() {
	a.server.mu.Lock()
	if a.server.localhostServer != nil {
		a.server.localhostServer.KickWebClients()
	}
	a.server.mu.Unlock()
}
