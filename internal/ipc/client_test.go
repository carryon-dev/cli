package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/holder"
)

// testEchoBackCommand returns a command that reads stdin and echoes it back.
// On Unix, this is "cat". On Windows, we use "findstr .*" which passes through all lines.
func testEchoBackCommand() string {
	if runtime.GOOS == "windows" {
		return `findstr ".*"`
	}
	return "cat"
}

// setupTestServer creates a fully wired server and returns the server + socket path.
// The caller is responsible for stopping the server and shutting down the session manager.
func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, socketPath, _ := setupTestServerWithCtx(t)
	return srv, socketPath
}

// setupTestServerWithCtx is like setupTestServer but also returns the RpcContext.
func setupTestServerWithCtx(t *testing.T) (*Server, string, *RpcContext) {
	t.Helper()
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("server Start failed: %v", err)
	}

	t.Cleanup(func() {
		ctx.SessionManager.Shutdown()
		srv.Stop()
	})

	return srv, socketPath, ctx
}

func TestClientConnectDisconnect(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	client.Disconnect()
}

func TestClientCall(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	result, err := client.Call("session.list", nil)
	if err != nil {
		t.Fatalf("Call session.list failed: %v", err)
	}

	// Result should be an empty list.
	switch v := result.(type) {
	case []any:
		if len(v) != 0 {
			t.Errorf("expected empty list, got %d items", len(v))
		}
	case nil:
		// null is acceptable for an empty slice
	default:
		t.Fatalf("expected array or nil result, got %T: %v", result, result)
	}
}

func TestClientCallError(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	_, err := client.Call("session.kill", map[string]any{
		"sessionId": "nonexistent-session-id",
	})
	if err == nil {
		t.Fatal("expected error from session.kill with nonexistent session, got nil")
	}
	// The error should mention the session not being found or backend not found.
	if !strings.Contains(err.Error(), "nonexistent") && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no backend") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestClientStreamAttach(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Create a session with an echo-back command.
	createResult, err := client.Call("session.create", map[string]any{
		"command": testEchoBackCommand(),
	})
	if err != nil {
		t.Fatalf("session.create failed: %v", err)
	}

	sessMap, ok := createResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", createResult, createResult)
	}
	sessionID, ok := sessMap["id"].(string)
	if !ok || sessionID == "" {
		t.Fatalf("expected non-empty session ID, got %v", sessMap["id"])
	}

	// Give the session a moment to start
	time.Sleep(100 * time.Millisecond)

	// Attach via RPC - for local sessions, returns holderSocket
	sockPath := attachAndGetSocket(t, client, sessionID)

	// Connect to the holder directly and verify I/O works
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	var received []byte
	var mu sync.Mutex
	done := make(chan struct{}, 1)

	hc.OnData(func(data []byte) {
		mu.Lock()
		received = append(received, data...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Write "hello\n" to the session via the holder.
	if err := hc.Write([]byte("hello\n")); err != nil {
		t.Fatalf("holder Write failed: %v", err)
	}

	// Wait for output containing "hello"
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-done:
			mu.Lock()
			got := string(received)
			mu.Unlock()
			if strings.Contains(got, "hello") {
				return // success
			}
		case <-deadline:
			mu.Lock()
			got := string(received)
			mu.Unlock()
			if strings.Contains(got, "hello") {
				return // success
			}
			t.Fatalf("expected output to contain 'hello', got %q", got)
		}
	}
}

// attachAndGetSocket calls session.attach and returns the holderSocket path.
func attachAndGetSocket(t *testing.T, client *Client, sessionID string) string {
	t.Helper()
	attachResult, err := client.Call("session.attach", map[string]any{
		"sessionId": sessionID,
	})
	if err != nil {
		t.Fatalf("session.attach failed: %v", err)
	}
	attachMap, ok := attachResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from session.attach, got %T", attachResult)
	}
	sockPath, ok := attachMap["holderSocket"].(string)
	if !ok || sockPath == "" {
		t.Fatalf("expected holderSocket in attach response, got %v", attachMap)
	}
	return sockPath
}

// writeViaHolder connects to the holder socket and writes data directly.
// Returns a HolderClient that the caller should close.
func writeViaHolder(t *testing.T, sockPath string, data string) *holder.HolderClient {
	t.Helper()
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	if err := hc.Write([]byte(data)); err != nil {
		hc.Close()
		t.Fatalf("holder Write failed: %v", err)
	}
	return hc
}

// --- Helper: create a session via client and return its ID ---

func createTestSession(t *testing.T, client *Client, command string) string {
	t.Helper()
	params := map[string]any{"command": command}
	result, err := client.Call("session.create", params)
	if err != nil {
		t.Fatalf("session.create failed: %v", err)
	}
	sessMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result from session.create, got %T", result)
	}
	id, ok := sessMap["id"].(string)
	if !ok || id == "" {
		t.Fatalf("expected non-empty session ID, got %v", sessMap["id"])
	}
	return id
}

// waitForOutput waits for the received buffer to contain substr within timeout.
func waitForOutput(mu *sync.Mutex, received *[]byte, substr string, done <-chan struct{}, timeout time.Duration) (string, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case <-done:
			mu.Lock()
			got := string(*received)
			mu.Unlock()
			if strings.Contains(got, substr) {
				return got, true
			}
		case <-deadline:
			mu.Lock()
			got := string(*received)
			mu.Unlock()
			return got, strings.Contains(got, substr)
		}
	}
}

// --- RPC method coverage tests ---

func TestClientSessionKill(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, "sleep 30")

	// Verify it appears in the list
	listResult, err := client.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list failed: %v", err)
	}
	sessions, ok := listResult.([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("expected 1 session before kill, got %v", listResult)
	}

	// Kill the session
	killResult, err := client.Call("session.kill", map[string]any{
		"sessionId": sessionID,
	})
	if err != nil {
		t.Fatalf("session.kill failed: %v", err)
	}
	killMap, ok := killResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from session.kill, got %T", killResult)
	}
	if killMap["ok"] != true {
		t.Errorf("expected ok:true from session.kill, got %v", killMap)
	}

	// Give the process a moment to clean up
	time.Sleep(100 * time.Millisecond)

	// Verify it is gone from the list
	listResult2, err := client.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list failed: %v", err)
	}
	switch v := listResult2.(type) {
	case []any:
		if len(v) != 0 {
			t.Errorf("expected 0 sessions after kill, got %d", len(v))
		}
	case nil:
		// empty is fine
	}
}

func TestClientSessionRename(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, "sleep 30")

	// Rename the session
	renameResult, err := client.Call("session.rename", map[string]any{
		"sessionId": sessionID,
		"name":      "renamed-session",
	})
	if err != nil {
		t.Fatalf("session.rename failed: %v", err)
	}
	renameMap, ok := renameResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from session.rename, got %T", renameResult)
	}
	if renameMap["ok"] != true {
		t.Errorf("expected ok:true from session.rename, got %v", renameMap)
	}

	// Verify the new name appears in list
	listResult, err := client.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list failed: %v", err)
	}
	sessions, ok := listResult.([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %v", listResult)
	}
	sess, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected session map, got %T", sessions[0])
	}
	if sess["name"] != "renamed-session" {
		t.Errorf("expected name 'renamed-session', got %q", sess["name"])
	}
}

func TestClientSessionResize(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, "sleep 30")
	time.Sleep(100 * time.Millisecond)

	// Attach (required for resize to have a PTY target)
	_, err := client.Call("session.attach", map[string]any{
		"sessionId": sessionID,
	})
	if err != nil {
		t.Fatalf("session.attach failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Resize via RPC
	resizeResult, err := client.Call("session.resize", map[string]any{
		"sessionId": sessionID,
		"cols":      float64(120),
		"rows":      float64(40),
	})
	if err != nil {
		t.Fatalf("session.resize failed: %v", err)
	}
	resizeMap, ok := resizeResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from session.resize, got %T", resizeResult)
	}
	if resizeMap["ok"] != true {
		t.Errorf("expected ok:true from session.resize, got %v", resizeMap)
	}
}

func TestClientSessionScrollback(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Use a command that produces known output but stays alive, so the
	// session still exists when we call scrollback.
	sessionID := createTestSession(t, client, "echo scrollback-test-marker; sleep 30")

	// Poll for the scrollback to contain the marker (the process needs time
	// to spawn and write output).
	deadline := time.After(3 * time.Second)
	for {
		scrollResult, err := client.Call("session.scrollback", map[string]any{
			"sessionId": sessionID,
		})
		if err != nil {
			t.Fatalf("session.scrollback failed: %v", err)
		}

		scrollback, ok := scrollResult.(string)
		if ok && strings.Contains(scrollback, "scrollback-test-marker") {
			return // success
		}

		select {
		case <-deadline:
			t.Fatalf("expected scrollback to contain 'scrollback-test-marker', got %q (type %T)", scrollResult, scrollResult)
		case <-time.After(100 * time.Millisecond):
			// retry
		}
	}
}

func TestClientConfigGet(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	result, err := client.Call("config.get", map[string]any{
		"key": "default.backend",
	})
	if err != nil {
		t.Fatalf("config.get failed: %v", err)
	}

	backend, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result from config.get default.backend, got %T: %v", result, result)
	}
	if backend != "native" {
		t.Errorf("expected default.backend to be 'native', got %q", backend)
	}
}

func TestClientConfigSet(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Set local.port to 9000
	setResult, err := client.Call("config.set", map[string]any{
		"key":   "local.port",
		"value": "9000",
	})
	if err != nil {
		t.Fatalf("config.set failed: %v", err)
	}
	setMap, ok := setResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from config.set, got %T", setResult)
	}
	if setMap["ok"] != true {
		t.Errorf("expected ok:true from config.set, got %v", setMap)
	}

	// Get it back and verify
	getResult, err := client.Call("config.get", map[string]any{
		"key": "local.port",
	})
	if err != nil {
		t.Fatalf("config.get failed: %v", err)
	}

	// JSON numbers come back as float64
	port, ok := getResult.(float64)
	if !ok {
		t.Fatalf("expected float64 from config.get, got %T: %v", getResult, getResult)
	}
	if int(port) != 9000 {
		t.Errorf("expected port 9000, got %v", port)
	}
}

func TestClientConfigReload(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Get the default port value first
	getResult, err := client.Call("config.get", map[string]any{
		"key": "local.port",
	})
	if err != nil {
		t.Fatalf("config.get failed: %v", err)
	}
	origPort, ok := getResult.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T: %v", getResult, getResult)
	}

	// Reload (resets in-memory values to defaults, then re-reads file)
	reloadResult, err := client.Call("config.reload", nil)
	if err != nil {
		t.Fatalf("config.reload failed: %v", err)
	}
	reloadMap, ok := reloadResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from config.reload, got %T", reloadResult)
	}
	if reloadMap["ok"] != true {
		t.Errorf("expected ok:true from config.reload, got %v", reloadMap)
	}

	// Verify the default value is preserved after reload (no file to override it)
	getResult2, err := client.Call("config.get", map[string]any{
		"key": "local.port",
	})
	if err != nil {
		t.Fatalf("config.get after reload failed: %v", err)
	}
	reloadedPort, ok := getResult2.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T: %v", getResult2, getResult2)
	}
	if int(reloadedPort) != int(origPort) {
		t.Errorf("expected port %v after reload, got %v", origPort, reloadedPort)
	}
}


func TestClientDaemonStatus(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	result, err := client.Call("daemon.status", nil)
	if err != nil {
		t.Fatalf("daemon.status failed: %v", err)
	}

	statusMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map from daemon.status, got %T: %v", result, result)
	}

	// Should have pid
	pid, ok := statusMap["pid"].(float64)
	if !ok || pid <= 0 {
		t.Errorf("expected positive pid, got %v", statusMap["pid"])
	}

	// Should have uptime
	uptime, ok := statusMap["uptime"].(float64)
	if !ok || uptime < 0 {
		t.Errorf("expected non-negative uptime, got %v", statusMap["uptime"])
	}
}

func TestClientDaemonLogs(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	result, err := client.Call("daemon.logs", nil)
	if err != nil {
		t.Fatalf("daemon.logs failed: %v", err)
	}

	logsMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map from daemon.logs, got %T: %v", result, result)
	}

	// Should have entries field (could be an empty array or an array with entries)
	entries, ok := logsMap["entries"]
	if !ok {
		t.Fatal("daemon.logs response missing 'entries' field")
	}

	// entries should be an array (possibly empty) or nil
	switch v := entries.(type) {
	case []any:
		// valid - could be empty or have entries from server setup
		_ = v
	case nil:
		// acceptable
	default:
		t.Fatalf("expected entries to be array or nil, got %T", entries)
	}
}

func TestClientProjectTerminalsCwdMatching(t *testing.T) {
	_, socketPath, rpcCtx := setupTestServerWithCtx(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	projectPath := filepath.Join(os.TempDir(), "test-project-cwd")

	// Inject a session directly into the session manager with cwd set.
	// This avoids actually spawning a process in the test environment.
	sess, err := rpcCtx.SessionManager.Create(backend.CreateOpts{
		Name:    "cwd-session",
		Cwd:     projectPath,
		Command: "cat",
	})
	if err != nil {
		t.Skipf("session create unavailable in this environment: %v", err)
	}

	// project.terminals should return it under "matched"
	termResult, err := client.Call("project.terminals", map[string]any{
		"path": projectPath,
	})
	if err != nil {
		t.Fatalf("project.terminals failed: %v", err)
	}
	termMap, ok := termResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map from project.terminals, got %T", termResult)
	}
	matched, ok := termMap["matched"].([]any)
	if !ok {
		t.Fatalf("expected matched to be array, got %T", termMap["matched"])
	}
	found := false
	for _, entry := range matched {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if m["id"] == sess.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected session %s to appear in matched, got %v", sess.ID, matched)
	}
}

// --- Stream integration tests ---

func TestClientStreamBidirectional(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, testEchoBackCommand())
	time.Sleep(100 * time.Millisecond)

	// Attach to get holder socket
	sockPath := attachAndGetSocket(t, client, sessionID)

	// Connect to holder directly
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	var received []byte
	var mu sync.Mutex
	done := make(chan struct{}, 10)

	hc.OnData(func(data []byte) {
		mu.Lock()
		received = append(received, data...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Write multiple things and verify each echoes back
	testStrings := []string{"alpha", "beta", "gamma"}
	for _, s := range testStrings {
		hc.Write([]byte(s + "\n"))
	}

	got, ok := waitForOutput(&mu, &received, "gamma", done, 3*time.Second)
	if !ok {
		t.Fatalf("expected output to contain 'gamma', got %q", got)
	}
	// Verify all strings were echoed
	for _, s := range testStrings {
		if !strings.Contains(got, s) {
			t.Errorf("expected output to contain %q, got %q", s, got)
		}
	}
}

func TestClientStreamResize(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, testEchoBackCommand())
	time.Sleep(100 * time.Millisecond)

	// Attach to get holder socket
	sockPath := attachAndGetSocket(t, client, sessionID)

	// Connect to holder directly
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	var received []byte
	var mu sync.Mutex
	done := make(chan struct{}, 10)

	hc.OnData(func(data []byte) {
		mu.Lock()
		received = append(received, data...)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Resize via holder client
	if err := hc.Resize(200, 50); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify the session still works after resize by writing and reading
	hc.Write([]byte("after-resize\n"))

	got, ok := waitForOutput(&mu, &received, "after-resize", done, 3*time.Second)
	if !ok {
		t.Fatalf("expected output to contain 'after-resize' after resize, got %q", got)
	}
}

func TestClientStreamClose(t *testing.T) {
	_, socketPath := setupTestServer(t)

	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	sessionID := createTestSession(t, client, "sleep 30")
	time.Sleep(100 * time.Millisecond)

	// Attach to get holder socket
	sockPath := attachAndGetSocket(t, client, sessionID)

	// Connect to holder and then close
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	hc.Close()
	time.Sleep(100 * time.Millisecond)

	// Verify session still exists in the list
	listResult, err := client.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list failed: %v", err)
	}

	sessions, ok := listResult.([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("expected 1 session after holder close, got %v", listResult)
	}

	sess, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected session map, got %T", sessions[0])
	}
	if sess["id"] != sessionID {
		t.Errorf("expected session ID %q, got %q", sessionID, sess["id"])
	}
}

func TestClientMultipleStreams(t *testing.T) {
	_, socketPath := setupTestServer(t)

	// Create a client for daemon interaction
	client := NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Create session
	sessionID := createTestSession(t, client, testEchoBackCommand())
	time.Sleep(100 * time.Millisecond)

	// Get holder socket via session.attach
	sockPath := attachAndGetSocket(t, client, sessionID)

	// Connect two holder clients directly
	hc1, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder client1 failed: %v", err)
	}
	defer hc1.Close()

	hc2, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder client2 failed: %v", err)
	}
	defer hc2.Close()

	var received1 []byte
	var mu1 sync.Mutex
	done1 := make(chan struct{}, 10)

	hc1.OnData(func(data []byte) {
		mu1.Lock()
		received1 = append(received1, data...)
		mu1.Unlock()
		select {
		case done1 <- struct{}{}:
		default:
		}
	})

	var received2 []byte
	var mu2 sync.Mutex
	done2 := make(chan struct{}, 10)

	hc2.OnData(func(data []byte) {
		mu2.Lock()
		received2 = append(received2, data...)
		mu2.Unlock()
		select {
		case done2 <- struct{}{}:
		default:
		}
	})

	// Write via client1's holder connection
	marker := fmt.Sprintf("multi-stream-%d", time.Now().UnixMilli())
	hc1.Write([]byte(marker + "\n"))

	// Both holder clients should receive the output
	got1, ok1 := waitForOutput(&mu1, &received1, marker, done1, 3*time.Second)
	if !ok1 {
		t.Errorf("client1 did not receive output containing %q, got %q", marker, got1)
	}

	got2, ok2 := waitForOutput(&mu2, &received2, marker, done2, 3*time.Second)
	if !ok2 {
		t.Errorf("client2 did not receive output containing %q, got %q", marker, got2)
	}
}
