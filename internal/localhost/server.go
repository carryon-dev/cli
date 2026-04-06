package localhost

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/session"
)

// WebClient tracks a connected web browser client.
type WebClient struct {
	ID          string `json:"clientId"`
	SessionID   string `json:"-"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	IP          string `json:"ip"`
	UserAgent   string `json:"userAgent"`
	ConnectedAt int64  `json:"connectedAt"`
}

//go:embed web/index.html
var indexHTML []byte

//go:embed web/login.html
var loginHTML string

//go:embed web/manifest.json
var manifestJSON []byte

var loginTmpl = template.Must(template.New("login").Parse(loginHTML))

// LocalhostServer serves an embedded web UI over HTTP (or HTTPS when
// exposed) and provides WebSocket access to terminal sessions.
type LocalhostServer struct {
	sessionManager *session.Manager
	logger         *logging.Logger
	auth           *AuthManager
	httpServer     *http.Server
	port           int
	expose         bool
	bind           string  // derived from expose
	baseDir        string  // ~/.carryon, for TLS cert storage
	useTLS         bool    // true when expose=true and TLS setup succeeds
	running        bool
	actualPort     int
	webClients      map[string]*WebClient   // clientID -> WebClient
	webClientCount  int
	broadcastFn     func(method string, params map[string]any)
	lastResizeConns map[string]*websocket.Conn // sessionID -> last conn that sent resize
	mu              sync.Mutex
}

// SetBroadcastFn sets the function used to broadcast events to IPC clients.
func (s *LocalhostServer) SetBroadcastFn(fn func(method string, params map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastFn = fn
}

// NewLocalhostServer creates a new localhost web server. When expose is true
// and baseDir is provided, the server uses self-signed TLS to protect session
// cookies on the network.
func NewLocalhostServer(sessionManager *session.Manager, logger *logging.Logger, port int, expose bool, baseDir string) *LocalhostServer {
	bind := "127.0.0.1"
	if expose {
		bind = "0.0.0.0"
	}
	return &LocalhostServer{
		sessionManager: sessionManager,
		logger:         logger,
		auth:           NewAuthManager(),
		port:           port,
		expose:         expose,
		bind:           bind,
		baseDir:        baseDir,
		webClients:      make(map[string]*WebClient),
		lastResizeConns: make(map[string]*websocket.Conn),
	}
}

// Start begins listening and serving HTTP + WebSocket connections.
func (s *LocalhostServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server already running")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.handleLoginPost(w, r)
		} else {
			s.handleLoginGet(w, r)
		}
	})
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Write(manifestJSON)
	})

	addr := fmt.Sprintf("%s:%d", s.bind, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Capture the actual port (important when port=0 for OS-assigned).
	tcpAddr := ln.Addr().(*net.TCPAddr)
	s.actualPort = tcpAddr.Port

	s.httpServer = &http.Server{
		Handler: mux,
	}

	// When exposed, wrap the listener to handle both TLS and plain HTTP
	// on the same port. TLS connections are handled normally; plain HTTP
	// gets a 301 redirect to HTTPS. Uses a self-signed certificate stored
	// in ~/.carryon/tls/.
	if s.expose && s.baseDir != "" {
		tlsConfig, fingerprint, tlsErr := loadOrGenerateTLS(s.baseDir)
		if tlsErr != nil {
			s.logger.Warn("localhost", fmt.Sprintf("TLS setup failed, falling back to HTTP: %v", tlsErr))
		} else {
			ln = &autoTLSListener{Listener: ln, tlsConfig: tlsConfig, port: s.actualPort}
			s.useTLS = true
			s.logger.Info("localhost", fmt.Sprintf("TLS enabled - cert fingerprint: %s", fingerprint))
		}
	}

	s.running = true
	scheme := "http"
	if s.useTLS {
		scheme = "https"
	}
	s.logger.Info("localhost", fmt.Sprintf("Web server listening on %s://%s:%d", scheme, s.bind, s.actualPort))
	if s.expose && !s.useTLS {
		s.logger.Warn("localhost", "Exposed without TLS - session cookies sent in cleartext")
	}

	go s.httpServer.Serve(ln)

	return nil
}

// Stop gracefully shuts down the HTTP server with a 5-second timeout.
func (s *LocalhostServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)
	s.logger.Info("localhost", "Web server stopped")
	return err
}

// Status returns the current server status.
func (s *LocalhostServer) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	port := s.actualPort
	if port == 0 {
		port = s.port
	}

	return map[string]any{
		"port":        port,
		"expose":      s.expose,
		"tls":         s.useTLS,
		"running":     s.running,
		"connections": 0,
	}
}

func (s *LocalhostServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}

	// Check for WebSocket upgrade on non-root paths.
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		if upgradeHeader := r.Header.Get("Upgrade"); upgradeHeader == "websocket" {
			s.handleWebSocket(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	// Check if this is a WebSocket upgrade request on root.
	if r.Header.Get("Upgrade") == "websocket" {
		s.handleWebSocket(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write(indexHTML)
}

func (s *LocalhostServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}

	sessions := s.sessionManager.List()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if sessions == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(sessions)
}

func (s *LocalhostServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}

	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "Missing session parameter", http.StatusBadRequest)
		return
	}

	acceptOpts := &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "[::1]:*"},
	}
	if s.expose {
		// Allow any origin when exposed (auth is enforced via session cookie).
		// Do NOT use InsecureSkipVerify - keep origin format validation.
		acceptOpts.OriginPatterns = append(acceptOpts.OriginPatterns, "*:*")
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		s.logger.Error("localhost", fmt.Sprintf("WebSocket accept error: %v", err))
		return
	}

	// Prepare web client metadata (not yet registered - registration happens
	// after setup that can fail, to avoid leaving stale entries).
	s.mu.Lock()
	s.webClientCount++
	clientID := fmt.Sprintf("web-%d", s.webClientCount)
	s.mu.Unlock()

	wc := &WebClient{
		ID:          clientID,
		SessionID:   sessionID,
		Type:        "web",
		Name:        parseUserAgent(r.UserAgent()),
		IP:          extractIP(r.RemoteAddr),
		UserAgent:   r.UserAgent(),
		ConnectedAt: time.Now().UnixMilli(),
	}

	// Send scrollback first.
	scrollback := s.sessionManager.GetScrollback(sessionID)
	if scrollback != "" {
		ctx := context.Background()
		if err := conn.Write(ctx, websocket.MessageBinary, []byte(scrollback)); err != nil {
			conn.Close(websocket.StatusInternalError, "scrollback write failed")
			return
		}
	}

	// Attach stream.
	stream, err := s.sessionManager.Attach(sessionID)
	if err != nil {
		conn.Close(websocket.StatusInternalError, err.Error())
		return
	}

	// All setup succeeded - register the web client now.
	s.mu.Lock()
	s.webClients[clientID] = wc
	bcast := s.broadcastFn
	s.mu.Unlock()

	s.logger.Info("localhost", fmt.Sprintf("WebSocket attached to %s (%s from %s)", sessionID, clientID, wc.IP))

	if bcast != nil {
		bcast("session.attached", map[string]any{
			"sessionId": sessionID,
			"clientId":  clientID,
		})
	}

	var closed bool
	var closeMu sync.Mutex

	cleanup := func() {
		closeMu.Lock()
		defer closeMu.Unlock()
		if closed {
			return
		}
		closed = true
		stream.Close()

		s.mu.Lock()
		delete(s.webClients, clientID)
		if s.lastResizeConns[sessionID] == conn {
			delete(s.lastResizeConns, sessionID)
		}
		bc := s.broadcastFn
		s.mu.Unlock()

		if bc != nil {
			bc("session.detached", map[string]any{
				"sessionId": sessionID,
				"clientId":  clientID,
			})
		}
	}

	// stream.OnData -> write to WebSocket as binary.
	listenerID := stream.OnData(func(data []byte) int {
		closeMu.Lock()
		isClosed := closed
		closeMu.Unlock()
		if isClosed {
			return 0
		}

		ctx := context.Background()
		if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
			cleanup()
			conn.Close(websocket.StatusInternalError, "write failed")
		}
		return 0
	})

	// Read loop: WebSocket message -> stream.Write or control message.
	go func() {
		defer func() {
			stream.OffData(listenerID)
			cleanup()
			conn.Close(websocket.StatusNormalClosure, "")
		}()

		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}

			closeMu.Lock()
			isClosed := closed
			closeMu.Unlock()
			if isClosed {
				return
			}

			if len(data) > 0 && data[0] == 0x01 {
				// Control message: parse JSON after the first byte.
				var msg struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if err := json.Unmarshal(data[1:], &msg); err == nil {
					if msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 && msg.Cols <= 65535 && msg.Rows <= 65535 {
						s.sessionManager.Resize(sessionID, uint16(msg.Cols), uint16(msg.Rows))
						s.mu.Lock()
						s.lastResizeConns[sessionID] = conn
						s.mu.Unlock()
					}
				}
			} else {
				// Terminal input - if this conn wasn't the last to resize
				// (or an external client resized, clearing the entry),
				// ask for current dimensions.
				s.mu.Lock()
				lastConn := s.lastResizeConns[sessionID]
				needsResize := lastConn != conn
				if needsResize {
					s.lastResizeConns[sessionID] = conn
				}
				s.mu.Unlock()

				if needsResize {
					ctrlMsg, _ := json.Marshal(map[string]string{"type": "resize_request"})
					payload := make([]byte, 1+len(ctrlMsg))
					payload[0] = 0x01
					copy(payload[1:], ctrlMsg)
					conn.Write(context.Background(), websocket.MessageBinary, payload)
				}

				stream.Write(data)
			}
		}
	}()
}

// requireAuth checks whether the request is authenticated. If auth is not
// required (localhost-only or no password set) it returns true immediately.
// Otherwise it validates the session cookie and returns false (after writing
// an appropriate HTTP response) when the request should be rejected.
func (s *LocalhostServer) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.auth.RequiresAuth(s.expose) {
		return true
	}

	// When exposed but no password configured, reject all requests.
	if !s.auth.HasPassword() {
		http.Error(w, "Server exposed but no password configured", http.StatusServiceUnavailable)
		return false
	}

	cookie, err := r.Cookie("carryon-session")
	if err == nil && s.auth.ValidateSession(cookie.Value) {
		return true
	}

	// Determine whether this is an API/WebSocket request or a browser page request.
	if r.Header.Get("Upgrade") == "websocket" ||
		strings.HasPrefix(r.URL.Path, "/api/") ||
		r.Header.Get("Accept") == "application/json" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}

	http.Redirect(w, r, "/login", http.StatusFound)
	return false
}

func (s *LocalhostServer) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	loginTmpl.Execute(w, struct{ Error string }{})
}

func (s *LocalhostServer) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")
	if !s.auth.CheckPassword(password) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		loginTmpl.Execute(w, struct{ Error string }{Error: "Invalid password"})
		return
	}

	token := s.auth.CreateSession(24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     "carryon-session",
		Value:    token,
		Path:     "/",
		MaxAge:   86400, // 24 hours - match server-side session TTL
		HttpOnly: true,
		Secure:   s.useTLS,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// SetPasswordHash sets the bcrypt password hash on the auth manager.
func (s *LocalhostServer) SetPasswordHash(hash string) {
	s.auth.SetPasswordHash(hash)
}

// ClearLastResize clears the last resize tracking for a session,
// so the next web client input will trigger a resize request.
// Called when an external source (e.g., CLI direct attach) resizes the PTY.
func (s *LocalhostServer) ClearLastResize(sessionID string) {
	s.mu.Lock()
	delete(s.lastResizeConns, sessionID)
	s.mu.Unlock()
}

// KickWebClients clears all auth sessions, forcing re-login.
func (s *LocalhostServer) KickWebClients() {
	s.auth.ClearSessions()
}

// GetSessionClients returns web clients attached to the given session.
func (s *LocalhostServer) GetSessionClients(sessionID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []map[string]any
	for _, wc := range s.webClients {
		if wc.SessionID == sessionID {
			result = append(result, map[string]any{
				"clientId":    wc.ID,
				"type":        wc.Type,
				"name":        wc.Name,
				"ip":          wc.IP,
				"pid":         0,
				"connectedAt": wc.ConnectedAt,
			})
		}
	}
	return result
}

// extractIP strips the port from a RemoteAddr string.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parseUserAgent extracts a readable browser + OS name from a User-Agent string.
func parseUserAgent(ua string) string {
	ua = strings.ToLower(ua)

	var browser, os string

	switch {
	case strings.Contains(ua, "firefox"):
		browser = "Firefox"
	case strings.Contains(ua, "edg/"):
		browser = "Edge"
	case strings.Contains(ua, "chrome") && !strings.Contains(ua, "edg/"):
		browser = "Chrome"
	case strings.Contains(ua, "safari") && !strings.Contains(ua, "chrome"):
		browser = "Safari"
	default:
		browser = "Browser"
	}

	switch {
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		os = "iOS"
	case strings.Contains(ua, "android"):
		os = "Android"
	case strings.Contains(ua, "mac os"):
		os = "macOS"
	case strings.Contains(ua, "windows"):
		os = "Windows"
	case strings.Contains(ua, "linux"):
		os = "Linux"
	}

	if os != "" {
		return browser + " " + os
	}
	return browser
}
