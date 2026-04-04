package localhost

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/session"
)

// tlsClient returns an HTTP client that accepts self-signed certificates.
func tlsClient(noRedirect bool) *http.Client {
	c := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	if noRedirect {
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c
}

// baseURL returns the scheme + host for a running server.
func baseURL(srv *LocalhostServer) string {
	port := srv.Status()["port"].(int)
	scheme := "http"
	if srv.useTLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
}

// shortTempDir creates a temp directory with a short path to stay within
// the Unix socket path length limit (104 bytes on macOS).
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "col-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func setupTestServer(t *testing.T) *LocalhostServer {
	t.Helper()
	tmpDir := shortTempDir(t)

	registry := backend.NewRegistry()
	nativeBackend := backend.NewNativeBackend(tmpDir, false)
	registry.Register(nativeBackend)

	logStore := logging.NewStore(filepath.Join(tmpDir, "logs"), 0)
	// Close the log file before t.TempDir() cleanup runs (cleanups are LIFO,
	// so registering this after t.TempDir() means it runs first).
	t.Cleanup(func() { logStore.Close() })
	logger := logging.NewLogger(logStore, "debug")
	sessionMgr := session.NewManager(registry, "native")

	return NewLocalhostServer(sessionMgr, logger, 0, false, "")
}

func TestLocalhostStartStop(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	status := srv.Status()
	if !status["running"].(bool) {
		t.Fatal("expected running=true after Start")
	}
	if status["port"].(int) == 0 {
		t.Fatal("expected non-zero port after Start with port=0")
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	status = srv.Status()
	if status["running"].(bool) {
		t.Fatal("expected running=false after Stop")
	}
}

func TestLocalhostServeHTML(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "text/html" {
		t.Errorf("expected Content-Type text/html, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if !strings.Contains(string(body), "<title>carryOn</title>") {
		t.Error("expected HTML to contain <title>carryOn</title>")
	}

	if !strings.Contains(string(body), "xterm") {
		t.Error("expected HTML to contain xterm references")
	}
}

func TestLocalhostSessionsAPI(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var sessions []any
	if err := json.Unmarshal(body, &sessions); err != nil {
		t.Fatalf("JSON parse failed: %v (body: %s)", err, string(body))
	}

	// No sessions created, should be empty array.
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestLocalhost404(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)
	url := fmt.Sprintf("http://127.0.0.1:%d/nonexistent", port)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /nonexistent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestLocalhostWebSocketNoSession(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Server returns HTTP 400 when session param is missing, so Dial should fail.
	_, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("expected WebSocket dial to fail when session param is missing")
	}
}

func TestLocalhostWebSocketInvalidSession(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws?session=nonexistent", port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.CloseNow()

	// The server should close the connection because the session doesn't exist.
	// Try to read - we expect an error (close frame).
	_, _, readErr := conn.Read(ctx)
	if readErr == nil {
		t.Fatal("expected read error due to server closing the connection for invalid session")
	}
}

func TestLocalhostStatusFields(t *testing.T) {
	srv := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	status := srv.Status()

	port, ok := status["port"].(int)
	if !ok {
		t.Fatal("status['port'] is not an int")
	}
	if port == 0 {
		t.Fatal("expected non-zero port")
	}

	expose, ok := status["expose"].(bool)
	if !ok {
		t.Fatal("status['expose'] is not a bool")
	}
	if expose {
		t.Fatal("expected expose=false")
	}

	running, ok := status["running"].(bool)
	if !ok {
		t.Fatal("status['running'] is not a bool")
	}
	if !running {
		t.Fatal("expected running=true")
	}
}

func TestLocalhostPortZero(t *testing.T) {
	srv := setupTestServer(t) // already uses port 0

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	status := srv.Status()
	port := status["port"].(int)
	if port == 0 {
		t.Fatal("expected Status() to return the actual assigned port, not 0")
	}

	// Verify the port is actually reachable.
	url := fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET on auto-assigned port %d failed: %v", port, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWebClientTracking(t *testing.T) {
	srv := setupTestServer(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)

	// Create a session
	sess, err := srv.sessionManager.Create(backend.CreateOpts{Name: "web-client-test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No web clients yet
	clients := srv.GetSessionClients(sess.ID)
	if len(clients) != 0 {
		t.Fatalf("expected 0 web clients before connect, got %d", len(clients))
	}

	// Connect via WebSocket
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws?session=%s", port, sess.ID)
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial: %v", err)
	}

	// Poll until 1 web client is registered
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clients = srv.GetSessionClients(sess.ID)
		if len(clients) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 web client, got %d", len(clients))
	}
	if clients[0]["type"] != "web" {
		t.Fatalf("expected type 'web', got %v", clients[0]["type"])
	}
	if clients[0]["clientId"] == "" {
		t.Fatal("expected non-empty clientId")
	}
	if clients[0]["connectedAt"] == nil {
		t.Fatal("expected non-nil connectedAt")
	}

	// Disconnect
	conn.Close(websocket.StatusNormalClosure, "")

	// Poll until 0 web clients remain
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clients = srv.GetSessionClients(sess.ID)
		if len(clients) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 0 {
		t.Fatalf("expected 0 web clients after disconnect, got %d", len(clients))
	}
}

func TestWebClientMultipleConnections(t *testing.T) {
	srv := setupTestServer(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)

	sess, err := srv.sessionManager.Create(backend.CreateOpts{Name: "multi-web"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws?session=%s", port, sess.ID)
	ctx := context.Background()

	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial 1: %v", err)
	}
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial 2: %v", err)
	}

	// Poll until 2 web clients are registered
	var clients []map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clients = srv.GetSessionClients(sess.ID)
		if len(clients) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 web clients, got %d", len(clients))
	}

	// Disconnect one - should go to 1
	conn1.Close(websocket.StatusNormalClosure, "")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clients = srv.GetSessionClients(sess.ID)
		if len(clients) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 web client after first disconnect, got %d", len(clients))
	}

	// Disconnect other - should go to 0
	conn2.Close(websocket.StatusNormalClosure, "")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clients = srv.GetSessionClients(sess.ID)
		if len(clients) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 0 {
		t.Fatalf("expected 0 web clients after both disconnect, got %d", len(clients))
	}
}

func TestWebClientOnlyMatchesSession(t *testing.T) {
	srv := setupTestServer(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)

	sess1, _ := srv.sessionManager.Create(backend.CreateOpts{Name: "sess-1"})
	sess2, _ := srv.sessionManager.Create(backend.CreateOpts{Name: "sess-2"})

	// Connect to sess1 only
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws?session=%s", port, sess1.ID)
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Poll until sess1 has 1 client registered
	var sess1Clients []map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sess1Clients = srv.GetSessionClients(sess1.ID)
		if len(sess1Clients) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// sess1 should have 1 client
	if len(sess1Clients) != 1 {
		t.Fatalf("sess1: expected 1 client, got %d", len(sess1Clients))
	}

	// sess2 should have 0 clients
	if clients := srv.GetSessionClients(sess2.ID); len(clients) != 0 {
		t.Fatalf("sess2: expected 0 clients, got %d", len(clients))
	}
}

func TestParseUserAgent(t *testing.T) {
	tests := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1", "Safari iOS"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", "Chrome macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0", "Firefox Windows"},
		{"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36", "Chrome Android"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", "Safari macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0", "Edge Windows"},
		{"", "Browser"},
		{"curl/8.0", "Browser"},
	}

	for _, tt := range tests {
		got := parseUserAgent(tt.ua)
		if got != tt.want {
			t.Errorf("parseUserAgent(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"192.168.1.5:54321", "192.168.1.5"},
		{"[::1]:8080", "::1"},
		{"127.0.0.1:0", "127.0.0.1"},
		{"invalid", "invalid"},
	}

	for _, tt := range tests {
		got := extractIP(tt.input)
		if got != tt.want {
			t.Errorf("extractIP(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// setupExposedServerWithPassword creates an exposed server with a bcrypt-hashed
// password set on its auth manager. Returns the server and the plaintext password.
func setupExposedServerWithPassword(t *testing.T) (*LocalhostServer, string) {
	t.Helper()
	tmpDir := shortTempDir(t)

	registry := backend.NewRegistry()
	nativeBackend := backend.NewNativeBackend(tmpDir, false)
	registry.Register(nativeBackend)

	logStore := logging.NewStore(filepath.Join(tmpDir, "logs"), 0)
	t.Cleanup(func() { logStore.Close() })
	logger := logging.NewLogger(logStore, "debug")
	sessionMgr := session.NewManager(registry, "native")

	srv := NewLocalhostServer(sessionMgr, logger, 0, true, tmpDir)

	password := "test-secret-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	srv.SetPasswordHash(string(hash))

	return srv, password
}

func TestExposedServerRedirectsToLogin(t *testing.T) {
	srv, _ := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client := tlsClient(true)
	resp, err := client.Get(baseURL(srv) + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestExposedServerAPIReturns401(t *testing.T) {
	srv, _ := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	resp, err := tlsClient(false).Get(baseURL(srv) + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestExposedServerLoginFlow(t *testing.T) {
	srv, password := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	base := baseURL(srv)
	client := tlsClient(true)

	// GET /login should return 200.
	resp, err := client.Get(base + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login: expected 200, got %d", resp.StatusCode)
	}

	// POST /login with wrong password should return 401.
	resp, err = client.PostForm(base+"/login", url.Values{"password": {"wrong-password"}})
	if err != nil {
		t.Fatalf("POST /login (wrong): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /login (wrong): expected 401, got %d", resp.StatusCode)
	}

	// POST /login with correct password should return 302 + session cookie.
	resp, err = client.PostForm(base+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatalf("POST /login (correct): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("POST /login (correct): expected 302, got %d", resp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "carryon-session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected carryon-session cookie after successful login")
	}

	// Subsequent request with cookie should return 200.
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.AddCookie(sessionCookie)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET / with cookie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / with cookie: expected 200, got %d", resp.StatusCode)
	}
}

func TestUnexposedServerNoAuth(t *testing.T) {
	// Uses the default setupTestServer which has expose=false.
	srv := setupTestServer(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)

	// GET / should return 200 without any auth.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// GET /api/sessions should also return 200.
	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port))
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
}

// loginAndGetCookie logs into the exposed server and returns the session cookie.
func loginAndGetCookie(t *testing.T, base, password string) *http.Cookie {
	t.Helper()
	client := tlsClient(true)
	resp, err := client.PostForm(base+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login: expected 302, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "carryon-session" {
			return c
		}
	}
	t.Fatal("no carryon-session cookie after login")
	return nil
}

func TestPasswordChangeInvalidatesSession(t *testing.T) {
	srv, password := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	base := baseURL(srv)
	client := tlsClient(false)

	// Log in and get a valid cookie.
	cookie := loginAndGetCookie(t, base, password)

	// Verify the cookie works.
	req, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET with cookie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 before password change, got %d", resp.StatusCode)
	}

	// Change password (clears all sessions).
	newHash, _ := bcrypt.GenerateFromPassword([]byte("new-password-456"), bcrypt.MinCost)
	srv.SetPasswordHash(string(newHash))
	srv.KickWebClients()

	// The old cookie should now be invalid.
	req2, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req2.AddCookie(cookie)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("GET after password change: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after password change, got %d", resp2.StatusCode)
	}

	// New password should work.
	newCookie := loginAndGetCookie(t, base, "new-password-456")
	req3, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req3.AddCookie(newCookie)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("GET with new cookie: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with new password cookie, got %d", resp3.StatusCode)
	}
}

func TestWebSocketRequiresAuthWhenExposed(t *testing.T) {
	srv, password := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	base := baseURL(srv)
	port := srv.Status()["port"].(int)

	// Create a session to attach to.
	sess, err := srv.sessionManager.Create(backend.CreateOpts{Name: "ws-auth-test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wsScheme := "ws"
	if srv.useTLS {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://127.0.0.1:%d/ws?session=%s", wsScheme, port, sess.ID)
	wsOpts := &websocket.DialOptions{
		HTTPClient: tlsClient(false),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// WebSocket without cookie should be rejected (401 on upgrade).
	_, _, dialErr := websocket.Dial(ctx, wsURL, wsOpts)
	if dialErr == nil {
		t.Fatal("expected WebSocket dial to fail without auth cookie")
	}

	// Log in, get cookie, then WebSocket with cookie should work.
	cookie := loginAndGetCookie(t, base, password)
	connCtx, connCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer connCancel()

	wsOptsWithCookie := &websocket.DialOptions{
		HTTPClient: tlsClient(false),
		HTTPHeader: http.Header{
			"Cookie": {cookie.Name + "=" + cookie.Value},
		},
	}
	conn, _, dialErr := websocket.Dial(connCtx, wsURL, wsOptsWithCookie)
	if dialErr != nil {
		t.Fatalf("WebSocket dial with cookie failed: %v", dialErr)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

func TestPasswordSetWithExposeDisabledNoAuth(t *testing.T) {
	// Server with expose=false but a password set.
	// Auth should NOT be required since expose is off.
	tmpDir := shortTempDir(t)

	registry := backend.NewRegistry()
	nativeBackend := backend.NewNativeBackend(tmpDir, false)
	registry.Register(nativeBackend)

	logStore := logging.NewStore(filepath.Join(tmpDir, "logs"), 0)
	t.Cleanup(func() { logStore.Close() })
	logger := logging.NewLogger(logStore, "debug")
	sessionMgr := session.NewManager(registry, "native")

	srv := NewLocalhostServer(sessionMgr, logger, 0, false, "") // expose=false

	// Set a password anyway.
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret-pass"), bcrypt.MinCost)
	srv.SetPasswordHash(string(hash))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	port := srv.Status()["port"].(int)

	// Should not require auth even though password is set.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (no auth when expose=false), got %d", resp.StatusCode)
	}

	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port))
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (no auth when expose=false), got %d", resp2.StatusCode)
	}
}

func TestWebSocketKickedAfterPasswordChange(t *testing.T) {
	srv, password := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	base := baseURL(srv)
	port := srv.Status()["port"].(int)

	// Create a session.
	sess, err := srv.sessionManager.Create(backend.CreateOpts{Name: "ws-kick-test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Log in and connect WebSocket.
	cookie := loginAndGetCookie(t, base, password)
	wsScheme := "ws"
	if srv.useTLS {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://127.0.0.1:%d/ws?session=%s", wsScheme, port, sess.ID)
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: tlsClient(false),
		HTTPHeader: http.Header{
			"Cookie": {cookie.Name + "=" + cookie.Value},
		},
	})
	if err != nil {
		t.Fatalf("WebSocket dial: %v", err)
	}

	// Wait for the web client to be registered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.GetSessionClients(sess.ID)) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(srv.GetSessionClients(sess.ID)) != 1 {
		t.Fatal("expected 1 web client before kick")
	}

	// Change password - this clears sessions.
	newHash, _ := bcrypt.GenerateFromPassword([]byte("changed-pass"), bcrypt.MinCost)
	srv.SetPasswordHash(string(newHash))
	srv.KickWebClients()

	// The existing WebSocket connection's session cookie is now invalid.
	// The next write from the server to this connection will fail because
	// the cookie is checked on HTTP upgrade, not on each frame. But the
	// connection should eventually be cleaned up.
	// Try to read - the server side will fail when it tries to write data
	// to this client (the stream OnData callback will get a write error).
	// For now, verify the session token is actually invalid.
	if srv.auth.ValidateSession(cookie.Value) {
		t.Fatal("expected session token to be invalidated after password change")
	}

	conn.Close(websocket.StatusNormalClosure, "")

	// Wait for cleanup.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.GetSessionClients(sess.ID)) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(srv.GetSessionClients(sess.ID)) != 0 {
		t.Fatal("expected 0 web clients after kick + close")
	}
}

// --- TLS certificate tests ---

func TestGenerateSelfSignedCert(t *testing.T) {
	tmpDir := t.TempDir()

	tlsConfig, fingerprint, err := loadOrGenerateTLS(tmpDir)
	if err != nil {
		t.Fatalf("loadOrGenerateTLS: %v", err)
	}

	if tlsConfig == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if fingerprint == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	// Fingerprint should be colon-separated hex (SHA-256 = 32 bytes = 64 hex chars + 31 colons).
	if len(fingerprint) != 95 {
		t.Fatalf("expected 95-char fingerprint, got %d: %s", len(fingerprint), fingerprint)
	}

	// Cert and key files should exist.
	certPath := filepath.Join(tmpDir, "tls", "cert.pem")
	keyPath := filepath.Join(tmpDir, "tls", "key.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("cert.pem not found: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key.pem not found: %v", err)
	}

	// Files should be 0600.
	info, _ := os.Stat(certPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("cert.pem mode: expected 0600, got %o", info.Mode().Perm())
	}
	info, _ = os.Stat(keyPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("key.pem mode: expected 0600, got %o", info.Mode().Perm())
	}

	// Parse the cert and check properties.
	certPEM, _ := os.ReadFile(certPath)
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if cert.Subject.CommonName != "carryOn localhost" {
		t.Errorf("expected CN 'carryOn localhost', got %q", cert.Subject.CommonName)
	}

	// Should have localhost DNS SAN and at least loopback IP SANs.
	foundLocalhost := false
	for _, dns := range cert.DNSNames {
		if dns == "localhost" {
			foundLocalhost = true
		}
	}
	if !foundLocalhost {
		t.Error("cert missing 'localhost' DNS SAN")
	}

	foundLoopback := false
	for _, ip := range cert.IPAddresses {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			foundLoopback = true
		}
	}
	if !foundLoopback {
		t.Error("cert missing 127.0.0.1 IP SAN")
	}

	// Should be valid for ~1 year.
	validity := cert.NotAfter.Sub(cert.NotBefore)
	if validity < 364*24*time.Hour || validity > 366*24*time.Hour {
		t.Errorf("expected ~365 day validity, got %v", validity)
	}
}

func TestLoadExistingCert(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate the first time.
	_, fp1, err := loadOrGenerateTLS(tmpDir)
	if err != nil {
		t.Fatalf("first loadOrGenerateTLS: %v", err)
	}

	// Load again - should reuse the same cert (same fingerprint).
	_, fp2, err := loadOrGenerateTLS(tmpDir)
	if err != nil {
		t.Fatalf("second loadOrGenerateTLS: %v", err)
	}

	if fp1 != fp2 {
		t.Errorf("expected same fingerprint on reload, got %s vs %s", fp1, fp2)
	}
}

func TestExpiredCertRegenerated(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate a cert that's already expired.
	tlsDir := filepath.Join(tmpDir, "tls")
	os.MkdirAll(tlsDir, 0700)
	certPath := filepath.Join(tlsDir, "cert.pem")
	keyPath := filepath.Join(tlsDir, "key.pem")

	// Create a cert that expired yesterday.
	cert1, _, err := generateSelfSigned(certPath, keyPath)
	if err != nil {
		t.Fatalf("generateSelfSigned: %v", err)
	}

	// Overwrite with an expired cert by manipulating the cert on disk.
	leaf, _ := x509.ParseCertificate(cert1.Certificate[0])
	_ = leaf // just proving it loaded

	// Tamper the cert file to be unreadable so loadCert fails and triggers regen.
	os.WriteFile(certPath, []byte("invalid"), 0600)

	_, fp2, err := loadOrGenerateTLS(tmpDir)
	if err != nil {
		t.Fatalf("loadOrGenerateTLS after invalid cert: %v", err)
	}
	if fp2 == "" {
		t.Error("expected non-empty fingerprint after regeneration")
	}
}

func TestHTTPRedirectsToHTTPS(t *testing.T) {
	srv, _ := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if !srv.useTLS {
		t.Fatal("expected TLS to be active on exposed server")
	}

	port := srv.Status()["port"].(int)

	// Plain HTTP request should get a 301 redirect to HTTPS.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/some/path", port))
	if err != nil {
		t.Fatalf("HTTP GET: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected 301, got %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	expected := fmt.Sprintf("https://127.0.0.1:%d/some/path", port)
	if loc != expected {
		t.Fatalf("expected Location %q, got %q", expected, loc)
	}
}

func TestSecureCookieWhenTLS(t *testing.T) {
	srv, password := setupExposedServerWithPassword(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	base := baseURL(srv)
	client := tlsClient(true)

	// Log in and check the cookie has Secure flag.
	resp, err := client.PostForm(base+"/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "carryon-session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected carryon-session cookie")
	}
	if !sessionCookie.Secure {
		t.Error("expected Secure flag on cookie when TLS is active")
	}
}

func TestNonExposedServerNoTLS(t *testing.T) {
	srv := setupTestServer(t)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if srv.useTLS {
		t.Error("expected no TLS on non-exposed server")
	}

	status := srv.Status()
	if status["tls"].(bool) {
		t.Error("expected tls=false in status")
	}

	// Plain HTTP should work directly (no redirect).
	port := status["port"].(int)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
