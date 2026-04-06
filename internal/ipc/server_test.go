package ipc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/session"
)

// mockRemoteService implements RemoteService for testing.
type mockRemoteService struct {
	statusFn        func() map[string]any
	connectFn       func() error
	disconnectFn    func()
	devicesFn       func() []map[string]any
	createSessionFn func(deviceID string, opts backend.CreateOpts) (string, error)
	attachSessionFn func(client *ClientState, sessionID string, ctx *RpcContext) error
}

func (m *mockRemoteService) Status() map[string]any {
	if m.statusFn != nil {
		return m.statusFn()
	}
	return map[string]any{"connected": false}
}
func (m *mockRemoteService) Connect() error {
	if m.connectFn != nil {
		return m.connectFn()
	}
	return nil
}
func (m *mockRemoteService) Disconnect() {
	if m.disconnectFn != nil {
		m.disconnectFn()
	}
}
func (m *mockRemoteService) Devices() []map[string]any {
	if m.devicesFn != nil {
		return m.devicesFn()
	}
	return []map[string]any{}
}
func (m *mockRemoteService) CreateSession(deviceID string, opts backend.CreateOpts) (string, error) {
	if m.createSessionFn != nil {
		return m.createSessionFn(deviceID, opts)
	}
	return "", fmt.Errorf("not implemented")
}
func (m *mockRemoteService) AttachSession(client *ClientState, sessionID string, ctx *RpcContext) error {
	if m.attachSessionFn != nil {
		return m.attachSessionFn(client, sessionID, ctx)
	}
	return fmt.Errorf("not implemented")
}

// mockLocalService implements LocalService for testing.
type mockLocalService struct {
	startFn       func() error
	stopFn        func() error
	statusFn      func() map[string]any
	setPasswordFn func(string)
	kickClientsFn func()
}

func (m *mockLocalService) Start() error {
	if m.startFn != nil {
		return m.startFn()
	}
	return nil
}
func (m *mockLocalService) Stop() error {
	if m.stopFn != nil {
		return m.stopFn()
	}
	return nil
}
func (m *mockLocalService) Status() map[string]any {
	if m.statusFn != nil {
		return m.statusFn()
	}
	return nil
}
func (m *mockLocalService) SetPassword(hash string) {
	if m.setPasswordFn != nil {
		m.setPasswordFn(hash)
	}
}
func (m *mockLocalService) KickWebClients() {
	if m.kickClientsFn != nil {
		m.kickClientsFn()
	}
}

// testSocketPath returns a platform-appropriate IPC socket/pipe path for tests.
// On Unix, it uses a short temp directory to stay within socket path limits.
// On Windows, it uses a unique named pipe path.
func testSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\carryon-test-%d`, time.Now().UnixNano())
	}
	dir, err := os.MkdirTemp("/tmp", "co-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}


// shortTempDir creates a temp directory with a short path to stay within
// the Unix socket path length limit (104 bytes on macOS).
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "coi-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// helper creates a fully wired RpcContext with a real NativeBackend.
func setupTestContext(t *testing.T) *RpcContext {
	t.Helper()
	tmpDir := shortTempDir(t)

	registry := backend.NewRegistry()
	nativeBackend := backend.NewNativeBackend(tmpDir, false)
	registry.Register(nativeBackend)

	logStore := logging.NewStore("", 0)
	logger := logging.NewLogger(logStore, "debug")
	cfgMgr := config.NewManager(filepath.Join(tmpDir, "config"))
	sessionMgr := session.NewManager(registry, "native")

	return &RpcContext{
		SessionManager: sessionMgr,
		Config:         cfgMgr,
		Logger:         logger,
		LogStore:       logStore,
		Registry:       registry,
		StartTime:      time.Now(),
		BaseDir:        tmpDir,
	}
}

// sendRpcRequest encodes a JSON-RPC request as a framed message, sends it
// over conn, and reads back the JSON-RPC response.
func sendRpcRequest(t *testing.T, socketPath string, method string, params map[string]any) map[string]any {
	t.Helper()

	conn, err := Dial(socketPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Build JSON-RPC request
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      1,
	}
	if params != nil {
		reqBody["params"] = params
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("json.Marshal request: %v", err)
	}

	// Encode as frame and send
	frame := EncodeFrame(Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "",
		Payload:   payload,
	})
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("conn.Write: %v", err)
	}

	// Read response frames (skip notifications - they have no "id" field).
	decoder := NewFrameDecoder()
	responseCh := make(chan Frame, 4)
	decoder.OnFrame(func(f Frame) {
		if f.Type == backend.FrameJsonRpc {
			// Only forward RPC responses (have "id"), skip notifications.
			var peek struct {
				ID any `json:"id"`
			}
			if json.Unmarshal(f.Payload, &peek) == nil && peek.ID != nil {
				responseCh <- f
			}
		}
	})

	buf := make([]byte, 65536)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for RPC response")
		case resp := <-responseCh:
			var result map[string]any
			if err := json.Unmarshal(resp.Payload, &result); err != nil {
				t.Fatalf("json.Unmarshal response: %v", err)
			}
			return result
		default:
			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if n > 0 {
				decoder.Push(buf[:n])
			}
			if err != nil {
				// Check for timeout - that's OK, keep trying
				if os.IsTimeout(err) {
					continue
				}
				t.Fatalf("conn.Read: %v", err)
			}
		}
	}
}

func TestServerStartStop(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify socket file exists (Unix only - Windows named pipes don't appear in the filesystem)
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			t.Fatal("socket file does not exist after Start")
		}
	}

	// Verify we can connect
	conn, err := Dial(socketPath)
	if err != nil {
		t.Fatalf("Dial failed after Start: %v", err)
	}
	conn.Close()

	// Stop server
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify socket file is removed (Unix only)
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
			t.Fatal("socket file still exists after Stop")
		}
	}
}

func TestRpcSessionList(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "session.list", nil)

	// Should have a result field
	result, ok := resp["result"]
	if !ok {
		t.Fatalf("response missing 'result' field: %v", resp)
	}

	// Result should be an empty list (or null for empty slice)
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

func TestRpcSessionCreateAndList(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		ctx.SessionManager.Shutdown()
		srv.Stop()
	}()

	// Create a session
	createResp := sendRpcRequest(t, socketPath, "session.create", map[string]any{
		"name":    "test-session",
		"command": "sleep 30",
	})

	if errField, ok := createResp["error"]; ok {
		t.Fatalf("session.create returned error: %v", errField)
	}

	createResult, ok := createResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result from session.create, got %T: %v", createResp["result"], createResp["result"])
	}

	sessionID, ok := createResult["id"].(string)
	if !ok || sessionID == "" {
		t.Fatalf("expected non-empty session ID, got %v", createResult["id"])
	}

	// Give the session a moment to register
	time.Sleep(50 * time.Millisecond)

	// List sessions
	listResp := sendRpcRequest(t, socketPath, "session.list", nil)

	listResult, ok := listResp["result"].([]any)
	if !ok {
		t.Fatalf("expected array result from session.list, got %T: %v", listResp["result"], listResp["result"])
	}

	if len(listResult) != 1 {
		t.Fatalf("expected 1 session, got %d", len(listResult))
	}

	sess, ok := listResult[0].(map[string]any)
	if !ok {
		t.Fatalf("expected session to be a map, got %T", listResult[0])
	}

	if sess["name"] != "test-session" {
		t.Errorf("session name: got %q, want %q", sess["name"], "test-session")
	}
}

func TestServerStreamAttachReturnsHolderSocket(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		ctx.SessionManager.Shutdown()
		srv.Stop()
	}()

	// Create a long-running session
	createResp := sendRpcRequest(t, socketPath, "session.create", map[string]any{
		"name":    "attach-test",
		"command": "sleep 30",
	})
	if errField, ok := createResp["error"]; ok {
		t.Fatalf("session.create returned error: %v", errField)
	}
	createResult, ok := createResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result from session.create, got %T", createResp["result"])
	}
	sessionID := createResult["id"].(string)

	// Give the session a moment to start
	time.Sleep(100 * time.Millisecond)

	// Attach to the session - should return holderSocket for local sessions
	attachResp := sendRpcRequest(t, socketPath, "session.attach", map[string]any{
		"sessionId": sessionID,
	})
	if errField, ok := attachResp["error"]; ok {
		t.Fatalf("session.attach returned error: %v", errField)
	}
	result, ok := attachResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result from session.attach, got %T", attachResp["result"])
	}
	holderSocket, ok := result["holderSocket"].(string)
	if !ok || holderSocket == "" {
		t.Fatalf("expected non-empty holderSocket in attach response, got %v", result)
	}
	returnedID, _ := result["sessionId"].(string)
	if returnedID != sessionID {
		t.Errorf("expected sessionId %s, got %s", sessionID, returnedID)
	}

	// Verify we should NOT get remote flag
	if result["remote"] == true {
		t.Error("expected no remote flag for local session")
	}
}

func TestClientStateRemoteBridgeInitiallyNil(t *testing.T) {
	cs := &ClientState{
		directSessions: make(map[string]struct{}),
		subscriptions:  make(map[string]func()),
	}
	bridge, sessionID := cs.GetRemoteBridge()
	if bridge != nil {
		t.Error("expected nil bridge initially")
	}
	if sessionID != "" {
		t.Errorf("expected empty session ID initially, got %q", sessionID)
	}
}

func TestClientStateSetGetRemoteBridge(t *testing.T) {
	cs := &ClientState{
		directSessions: make(map[string]struct{}),
		subscriptions:  make(map[string]func()),
	}

	// Set with a nil bridge but non-empty session ID - tests the session ID round-trip.
	cs.SetRemoteBridge(nil, "remote-sess-1")
	_, sessionID := cs.GetRemoteBridge()
	if sessionID != "remote-sess-1" {
		t.Errorf("expected remote-sess-1, got %q", sessionID)
	}

	// Overwrite with a different session ID.
	cs.SetRemoteBridge(nil, "remote-sess-2")
	_, sessionID = cs.GetRemoteBridge()
	if sessionID != "remote-sess-2" {
		t.Errorf("expected remote-sess-2, got %q", sessionID)
	}

	// Clear by setting an empty session ID.
	cs.SetRemoteBridge(nil, "")
	_, sessionID = cs.GetRemoteBridge()
	if sessionID != "" {
		t.Errorf("expected empty session ID after clear, got %q", sessionID)
	}
}

func TestClientStateWriteIpcFrame(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	cs := &ClientState{
		conn:          server,
		subscriptions: make(map[string]func()),
	}

	data := []byte("test frame data")

	errCh := make(chan error, 1)
	go func() {
		errCh <- cs.WriteIpcFrame(data)
	}()

	buf := make([]byte, len(data))
	_, err := io.ReadFull(client, buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if !bytes.Equal(buf, data) {
		t.Errorf("expected %q, got %q", data, buf)
	}

	if err := <-errCh; err != nil {
		t.Errorf("WriteIpcFrame error: %v", err)
	}
}

func TestRpcConfigGetSet(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Get default port
	getResp := sendRpcRequest(t, socketPath, "config.get", map[string]any{
		"key": "local.port",
	})

	if errField, ok := getResp["error"]; ok {
		t.Fatalf("config.get returned error: %v", errField)
	}

	// Default port should be 8384
	result := getResp["result"]
	switch v := result.(type) {
	case float64:
		if int(v) != 8384 {
			t.Errorf("default port: got %v, want 8384", v)
		}
	default:
		t.Fatalf("expected numeric result, got %T: %v", result, result)
	}

	// Set port to 9000
	setResp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "local.port",
		"value": "9000",
	})

	if errField, ok := setResp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}

	// Get port again - should be 9000
	getResp2 := sendRpcRequest(t, socketPath, "config.get", map[string]any{
		"key": "local.port",
	})

	result2 := getResp2["result"]
	switch v := result2.(type) {
	case float64:
		if int(v) != 9000 {
			t.Errorf("updated port: got %v, want 9000", v)
		}
	default:
		t.Fatalf("expected numeric result, got %T: %v", result2, result2)
	}
}

func TestRpcConfigSchema(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "config.schema", nil)

	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.schema returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", resp["result"], resp["result"])
	}

	// Check schemaVersion
	version, ok := result["schemaVersion"].(float64)
	if !ok || int(version) != 1 {
		t.Fatalf("expected schemaVersion 1, got %v", result["schemaVersion"])
	}

	// Check groups exist
	groups, ok := result["groups"].([]any)
	if !ok {
		t.Fatalf("expected groups array, got %T", result["groups"])
	}
	if len(groups) != 5 {
		t.Fatalf("expected 5 groups, got %d", len(groups))
	}

	// Verify first group is "default"
	firstGroup, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("expected group to be map, got %T", groups[0])
	}
	if firstGroup["key"] != "default" {
		t.Errorf("expected first group key 'default', got %q", firstGroup["key"])
	}

	// Verify settings exist in the first group
	settings, ok := firstGroup["settings"].([]any)
	if !ok {
		t.Fatalf("expected settings array, got %T", firstGroup["settings"])
	}
	if len(settings) < 1 {
		t.Fatal("expected at least 1 setting in default group")
	}

	// Verify first setting has expected fields
	firstSetting, ok := settings[0].(map[string]any)
	if !ok {
		t.Fatalf("expected setting to be map, got %T", settings[0])
	}
	for _, field := range []string{"key", "name", "description", "type", "default", "value"} {
		if _, ok := firstSetting[field]; !ok {
			t.Errorf("setting missing field %q", field)
		}
	}
}

func TestRpcConfigSetBroadcastsChange(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	// Track broadcasts - set after NewServer so it does not get overwritten.
	broadcasts := make(chan map[string]any, 10)
	ctx.BroadcastFn = func(method string, params map[string]any) {
		broadcasts <- map[string]any{"method": method, "params": params}
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Set a config value
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "logs.level",
		"value": "debug",
	})

	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}

	// Check that a config.changed broadcast was sent
	select {
	case msg := <-broadcasts:
		if msg["method"] != "config.changed" {
			t.Fatalf("expected config.changed broadcast, got %v", msg["method"])
		}
		params, ok := msg["params"].(map[string]any)
		if !ok {
			t.Fatalf("expected params map, got %T", msg["params"])
		}
		if params["key"] != "logs.level" {
			t.Errorf("expected key 'logs.level', got %v", params["key"])
		}
		if params["value"] != "debug" {
			t.Errorf("expected value 'debug', got %v", params["value"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for config.changed broadcast")
	}
}

func TestRpcConfigSetSideEffectLocalEnabled(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	// Override Local after NewServer so it isn't the real one.
	startCalled := make(chan struct{}, 1)
	stopCalled := make(chan struct{}, 1)
	ctx.Local = &mockLocalService{
		startFn: func() error {
			startCalled <- struct{}{}
			return nil
		},
		stopFn: func() error {
			stopCalled <- struct{}{}
			return nil
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Setting local.enabled to true should trigger LocalhostStart.
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "true",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}
	select {
	case <-startCalled:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Local.Start was not called after setting local.enabled=true")
	}

	// Setting local.enabled to false should trigger LocalhostStop.
	resp = sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "false",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}
	select {
	case <-stopCalled:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Local.Stop was not called after setting local.enabled=false")
	}
}

func TestRpcConfigSetSideEffectLogLevel(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// The test context starts with log level "debug", so set it to "error" first
	// to ensure debug messages are filtered, then set it to "debug" via config.set.
	ctx.Logger.SetLevel("error")

	// Verify that debug messages are filtered at error level.
	ctx.Logger.Debug("test", "should-be-filtered")
	entries := ctx.LogStore.GetRecent(100)
	for _, e := range entries {
		if e.Message == "should-be-filtered" {
			t.Fatal("debug message should be filtered at error level")
		}
	}

	// Now set logs.level to debug via config.set.
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "logs.level",
		"value": "debug",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}

	// Now a debug message should be recorded.
	ctx.Logger.Debug("test", "after-level-change")
	entries = ctx.LogStore.GetRecent(100)
	found := false
	for _, e := range entries {
		if e.Message == "after-level-change" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("debug message was not recorded after setting logs.level=debug")
	}
}

func TestRpcConfigSetNoSideEffectForNormalKeys(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	// Override Local after NewServer to track calls.
	startCalled := false
	ctx.Local = &mockLocalService{
		startFn: func() error {
			startCalled = true
			return nil
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Set a normal config key that should NOT trigger any side effect.
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "default.backend",
		"value": "native",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}

	if startCalled {
		t.Fatal("Local.Start should not be called for default.backend")
	}
}

func TestRpcLocalRpcsRemoved(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	for _, method := range []string{"local.enable", "local.disable", "local.status"} {
		resp := sendRpcRequest(t, socketPath, method, nil)
		errField, ok := resp["error"]
		if !ok {
			t.Errorf("%s should return error, got result: %v", method, resp["result"])
			continue
		}
		errMap, ok := errField.(map[string]any)
		if !ok {
			t.Errorf("%s error field is not a map: %T", method, errField)
			continue
		}
		msg, _ := errMap["message"].(string)
		if msg == "" {
			t.Errorf("%s should return error message", method)
		}
	}
}

func TestRpcConfigSetSideEffectRemoteEnabled(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	// Override Remote after NewServer so it isn't the real one.
	connectCalled := make(chan struct{}, 1)
	disconnectCalled := make(chan struct{}, 1)
	ctx.Remote = &mockRemoteService{
		connectFn: func() error {
			connectCalled <- struct{}{}
			return nil
		},
		disconnectFn: func() {
			disconnectCalled <- struct{}{}
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Setting remote.enabled to true should trigger RemoteConnect.
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "remote.enabled",
		"value": "true",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}
	select {
	case <-connectCalled:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Remote.Connect was not called after setting remote.enabled=true")
	}

	// Setting remote.enabled to false should trigger RemoteDisconnect.
	resp = sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "remote.enabled",
		"value": "false",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}
	select {
	case <-disconnectCalled:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Remote.Disconnect was not called after setting remote.enabled=false")
	}
}

func TestRpcRemovedBackendAndRemoteMethods(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	for _, method := range []string{"backend.list", "remote.connect", "remote.disconnect", "remote.rename"} {
		resp := sendRpcRequest(t, socketPath, method, nil)
		errField, ok := resp["error"]
		if !ok {
			t.Errorf("%s should return error, got result: %v", method, resp["result"])
			continue
		}
		errMap, ok := errField.(map[string]any)
		if !ok {
			t.Errorf("%s error field is not a map: %T", method, errField)
			continue
		}
		msg, _ := errMap["message"].(string)
		if msg == "" {
			t.Errorf("%s should return error message", method)
		}
	}
}

func TestRpcDaemonStatusIncludesLocal(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "daemon.status", nil)
	if errField, ok := resp["error"]; ok {
		t.Fatalf("daemon.status returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp["result"])
	}

	local, ok := result["local"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'local' field in daemon.status, got %T", result["local"])
	}

	running, ok := local["running"].(bool)
	if !ok {
		t.Fatalf("expected 'running' bool in local status, got %T", local["running"])
	}
	if running {
		t.Error("expected local server not running in test context")
	}

	port, ok := local["port"].(float64)
	if !ok {
		t.Fatalf("expected 'port' number in local status, got %T", local["port"])
	}
	if int(port) != 8384 {
		t.Errorf("expected default port 8384, got %v", port)
	}

	expose, ok := local["expose"].(bool)
	if !ok {
		t.Fatalf("expected 'expose' bool in local status, got %T", local["expose"])
	}
	if expose {
		t.Error("expected default expose=false")
	}

	_, ok = local["enabled"]
	if !ok {
		t.Fatal("expected 'enabled' field in local status")
	}
}

func TestRpcRemoteStatusNotConfigured(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "remote.status", nil)
	if errField, ok := resp["error"]; ok {
		t.Fatalf("remote.status returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp["result"])
	}

	connected, ok := result["connected"].(bool)
	if !ok {
		t.Fatalf("expected 'connected' bool, got %T", result["connected"])
	}
	if connected {
		t.Error("expected connected=false when remote is not configured")
	}
}

// --- sessionCreate and sessionAttach unit tests (call handlers directly) ---

func TestSessionCreateRemote(t *testing.T) {
	ctx := setupTestContext(t)

	var calledDeviceID string
	var calledOpts backend.CreateOpts
	ctx.Remote = &mockRemoteService{
		createSessionFn: func(deviceID string, opts backend.CreateOpts) (string, error) {
			calledDeviceID = deviceID
			calledOpts = opts
			return "remote-session-123", nil
		},
	}

	result, err := sessionCreate(map[string]any{
		"device_id": "dev-abc",
		"name":      "test",
		"cwd":       "/home/user",
	}, ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calledDeviceID != "dev-abc" {
		t.Errorf("expected device ID dev-abc, got %s", calledDeviceID)
	}
	if calledOpts.Name != "test" {
		t.Errorf("expected opts.Name test, got %s", calledOpts.Name)
	}
	if calledOpts.Cwd != "/home/user" {
		t.Errorf("expected opts.Cwd /home/user, got %s", calledOpts.Cwd)
	}

	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result.Value)
	}
	if val["id"] != "remote-session-123" {
		t.Errorf("expected id remote-session-123, got %v", val["id"])
	}
	if val["remote"] != true {
		t.Errorf("expected remote=true, got %v", val["remote"])
	}
	if val["device_id"] != "dev-abc" {
		t.Errorf("expected device_id dev-abc, got %v", val["device_id"])
	}
}

func TestSessionCreateRemoteError(t *testing.T) {
	ctx := setupTestContext(t)

	ctx.Remote = &mockRemoteService{
		createSessionFn: func(deviceID string, opts backend.CreateOpts) (string, error) {
			return "", fmt.Errorf("remote unavailable")
		},
	}

	_, err := sessionCreate(map[string]any{
		"device_id": "dev-abc",
		"name":      "test",
	}, ctx)

	if err == nil {
		t.Fatal("expected error from remote callback, got nil")
	}
	if err.Error() != "remote unavailable" {
		t.Errorf("expected 'remote unavailable', got %q", err.Error())
	}
}

func TestSessionCreateRemoteNotConnected(t *testing.T) {
	ctx := setupTestContext(t)
	// ctx.Remote is nil - simulate not connected

	_, err := sessionCreate(map[string]any{
		"device_id": "dev-abc",
		"name":      "test",
	}, ctx)

	if err == nil {
		t.Fatal("expected error when remote not connected, got nil")
	}
	if err.Error() != "remote not connected" {
		t.Errorf("expected 'remote not connected', got %q", err.Error())
	}
}

func TestSessionCreateLocal(t *testing.T) {
	ctx := setupTestContext(t)
	defer ctx.SessionManager.Shutdown()

	result, err := sessionCreate(map[string]any{
		"name":    "local-session",
		"command": "sleep 30",
	}, ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Local creation returns a backend.Session (not a map with remote:true)
	sess, ok := result.Value.(backend.Session)
	if !ok {
		t.Fatalf("expected backend.Session, got %T: %v", result.Value, result.Value)
	}
	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if sess.Name != "local-session" {
		t.Errorf("expected name local-session, got %s", sess.Name)
	}
}

func TestSessionAttachLocalFound(t *testing.T) {
	ctx := setupTestContext(t)
	defer ctx.SessionManager.Shutdown()

	// Create a local session
	created, err := sessionCreate(map[string]any{
		"name":    "attach-test",
		"command": "sleep 30",
	}, ctx)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess := created.Value.(backend.Session)

	result, err := sessionAttach(map[string]any{
		"sessionId": sess.ID,
	}, ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result.Value)
	}
	if val["sessionId"] != sess.ID {
		t.Errorf("expected sessionId %s, got %v", sess.ID, val["sessionId"])
	}
	holderSocket, _ := val["holderSocket"].(string)
	if holderSocket == "" {
		t.Error("expected non-empty holderSocket for local session")
	}
	if val["remote"] == true {
		t.Error("expected remote to not be true for local session")
	}
}

func TestSessionAttachRemoteFallthrough(t *testing.T) {
	ctx := setupTestContext(t)

	var calledSessionID string
	ctx.Remote = &mockRemoteService{
		attachSessionFn: func(client *ClientState, sessionID string, rpcCtx *RpcContext) error {
			calledSessionID = sessionID
			return nil
		},
	}

	result, err := sessionAttach(map[string]any{
		"sessionId": "nonexistent-session-xyz",
	}, ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result.Value)
	}
	if val["remote"] != true {
		t.Errorf("expected remote=true for remote attach, got %v", val["remote"])
	}
	if val["streamId"] != "nonexistent-session-xyz" {
		t.Errorf("expected streamId nonexistent-session-xyz, got %v", val["streamId"])
	}

	// Invoke the PostRPC to confirm the callback fires with the correct session ID
	if result.PostRPC != nil {
		result.PostRPC(nil)
	}
	if calledSessionID != "nonexistent-session-xyz" {
		t.Errorf("expected Remote.AttachSession called with nonexistent-session-xyz, got %q", calledSessionID)
	}
}

func TestSessionAttachNotFoundAnywhere(t *testing.T) {
	ctx := setupTestContext(t)
	// ctx.Remote is nil - no fallback

	_, err := sessionAttach(map[string]any{
		"sessionId": "ghost-session",
	}, ctx)

	if err == nil {
		t.Fatal("expected error when session not found, got nil")
	}
	if err.Error() != "session not found: ghost-session" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

// TestSubscribeCancelOnlyCancelsTargetSubscription verifies that subscribe.cancel
// cancels only the specific subscription and leaves other subscriptions intact.
func TestRpcRemoteDevices(t *testing.T) {
	t.Run("nil remote returns empty array", func(t *testing.T) {
		socketPath := testSocketPath(t)
		ctx := setupTestContext(t)
		// ctx.Remote is nil

		srv := NewServer(socketPath, ctx)
		if err := srv.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer srv.Stop()

		resp := sendRpcRequest(t, socketPath, "remote.devices", nil)
		if errField, ok := resp["error"]; ok {
			t.Fatalf("remote.devices returned error: %v", errField)
		}

		result, ok := resp["result"].([]any)
		if !ok {
			t.Fatalf("expected array result, got %T: %v", resp["result"], resp["result"])
		}
		if len(result) != 0 {
			t.Errorf("expected empty array, got %d items", len(result))
		}
	})

	t.Run("returns devices from mock", func(t *testing.T) {
		socketPath := testSocketPath(t)
		ctx := setupTestContext(t)

		ctx.Remote = &mockRemoteService{
			devicesFn: func() []map[string]any {
				return []map[string]any{
					{
						"id":        "dev-1",
						"name":      "Laptop",
						"online":    true,
						"team_id":   "team-a",
						"team_name": "Team A",
						"sessions":  []map[string]any{},
					},
					{
						"id":        "dev-2",
						"name":      "Desktop",
						"online":    false,
						"team_id":   "team-a",
						"team_name": "Team A",
						"sessions":  []map[string]any{},
					},
				}
			},
		}

		srv := NewServer(socketPath, ctx)
		if err := srv.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer srv.Stop()

		resp := sendRpcRequest(t, socketPath, "remote.devices", nil)
		if errField, ok := resp["error"]; ok {
			t.Fatalf("remote.devices returned error: %v", errField)
		}

		result, ok := resp["result"].([]any)
		if !ok {
			t.Fatalf("expected array result, got %T: %v", resp["result"], resp["result"])
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 devices, got %d", len(result))
		}

		dev1, ok := result[0].(map[string]any)
		if !ok {
			t.Fatalf("expected device to be a map, got %T", result[0])
		}
		if dev1["id"] != "dev-1" {
			t.Errorf("first device id: got %q, want %q", dev1["id"], "dev-1")
		}
		if dev1["name"] != "Laptop" {
			t.Errorf("first device name: got %q, want %q", dev1["name"], "Laptop")
		}

		dev2, ok := result[1].(map[string]any)
		if !ok {
			t.Fatalf("expected device to be a map, got %T", result[1])
		}
		if dev2["id"] != "dev-2" {
			t.Errorf("second device id: got %q, want %q", dev2["id"], "dev-2")
		}
	})
}

func TestRpcRemoteStatusConnected(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	ctx.Remote = &mockRemoteService{
		statusFn: func() map[string]any {
			return map[string]any{
				"connected": true,
				"device_id": "dev-1",
			}
		},
	}

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "remote.status", nil)
	if errField, ok := resp["error"]; ok {
		t.Fatalf("remote.status returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", resp["result"], resp["result"])
	}
	connected, ok := result["connected"].(bool)
	if !ok {
		t.Fatalf("expected 'connected' bool, got %T", result["connected"])
	}
	if !connected {
		t.Error("expected connected=true")
	}
	if result["device_id"] != "dev-1" {
		t.Errorf("device_id: got %q, want %q", result["device_id"], "dev-1")
	}
}

func TestRpcLocalSetPassword(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	setPasswordCalled := false
	kickClientsCalled := false
	ctx.Local = &mockLocalService{
		setPasswordFn: func(hash string) {
			setPasswordCalled = true
			if hash == "" {
				t.Error("expected non-empty password hash")
			}
		},
		kickClientsFn: func() {
			kickClientsCalled = true
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	resp := sendRpcRequest(t, socketPath, "local.set-password", map[string]any{
		"password": "test1234",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("local.set-password returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp["result"])
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
	if !setPasswordCalled {
		t.Error("expected SetPassword to be called")
	}
	if !kickClientsCalled {
		t.Error("expected KickWebClients to be called")
	}
}

func TestRpcConfigSetSideEffectLocalPort(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)

	var callOrder []string
	ctx.Local = &mockLocalService{
		startFn: func() error {
			callOrder = append(callOrder, "start")
			return nil
		},
		stopFn: func() error {
			callOrder = append(callOrder, "stop")
			return nil
		},
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// First enable local so port changes trigger restart
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "true",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set local.enabled returned error: %v", errField)
	}

	// Reset call tracking after the enable side effect
	callOrder = nil

	// Now change port - should trigger stop then start (restart)
	resp = sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "local.port",
		"value": "9999",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set local.port returned error: %v", errField)
	}

	if len(callOrder) != 2 {
		t.Fatalf("expected 2 calls (stop, start), got %d: %v", len(callOrder), callOrder)
	}
	if callOrder[0] != "stop" {
		t.Errorf("expected first call to be stop, got %s", callOrder[0])
	}
	if callOrder[1] != "start" {
		t.Errorf("expected second call to be start, got %s", callOrder[1])
	}
}

func TestRpcConfigSetSideEffectRemoteEnabledNoRemote(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	// ctx.Remote is deliberately nil

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Setting remote.enabled=true with nil Remote should not panic
	resp := sendRpcRequest(t, socketPath, "config.set", map[string]any{
		"key":   "remote.enabled",
		"value": "true",
	})
	if errField, ok := resp["error"]; ok {
		t.Fatalf("config.set returned error: %v", errField)
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp["result"])
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
}

func TestSubscribeCancelOnlyCancelsTargetSubscription(t *testing.T) {
	socketPath := testSocketPath(t)
	ctx := setupTestContext(t)

	srv := NewServer(socketPath, ctx)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Track which subscriptions have been cancelled via their unsub functions.
	cancelledSub1 := false
	cancelledSub2 := false

	// Inject two subscriptions directly into the server's client map by
	// connecting a client and then manipulating its subscriptions map.
	conn, err := Dial(socketPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Give the server a moment to register the client.
	time.Sleep(20 * time.Millisecond)

	// Find the connected client and inject two subscriptions manually.
	srv.mu.Lock()
	var targetClient *ClientState
	for _, c := range srv.clients {
		targetClient = c
		break
	}
	if targetClient != nil {
		targetClient.mu.Lock()
		targetClient.subscriptions["sub-1"] = func() { cancelledSub1 = true }
		targetClient.subscriptions["sub-2"] = func() { cancelledSub2 = true }
		targetClient.mu.Unlock()
	}
	srv.mu.Unlock()

	if targetClient == nil {
		t.Fatal("no client connected to server")
	}

	// Cancel only sub-1 via RPC.
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "subscribe.cancel",
		"id":      1,
		"params":  map[string]any{"subscriptionId": "sub-1"},
	}
	payload, _ := json.Marshal(reqBody)
	frame := EncodeFrame(Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "",
		Payload:   payload,
	})
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("conn.Write: %v", err)
	}

	// Read the response to ensure the RPC completed.
	decoder := NewFrameDecoder()
	responseCh := make(chan struct{}, 1)
	decoder.OnFrame(func(f Frame) {
		if f.Type == backend.FrameJsonRpc {
			var peek struct{ ID any `json:"id"` }
			if json.Unmarshal(f.Payload, &peek) == nil && peek.ID != nil {
				select {
				case responseCh <- struct{}{}:
				default:
				}
			}
		}
	})
	buf := make([]byte, 4096)
	deadline := time.After(3 * time.Second)
outer:
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for subscribe.cancel response")
		case <-responseCh:
			break outer
		default:
			conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			n, readErr := conn.Read(buf)
			if n > 0 {
				decoder.Push(buf[:n])
			}
			if readErr != nil && !os.IsTimeout(readErr) {
				t.Fatalf("conn.Read: %v", readErr)
			}
		}
	}

	// sub-1 should have been cancelled.
	if !cancelledSub1 {
		t.Error("expected sub-1 to be cancelled")
	}

	// sub-2 must still be active - cancelling sub-1 must not affect sub-2.
	if cancelledSub2 {
		t.Error("sub-2 should NOT be cancelled when only sub-1 was cancelled")
	}

	// sub-2 should still exist in the client's subscriptions map.
	targetClient.mu.Lock()
	_, sub2exists := targetClient.subscriptions["sub-2"]
	_, sub1exists := targetClient.subscriptions["sub-1"]
	targetClient.mu.Unlock()

	if !sub2exists {
		t.Error("sub-2 should still exist in client subscriptions after cancelling sub-1")
	}
	if sub1exists {
		t.Error("sub-1 should have been removed from client subscriptions")
	}
}

