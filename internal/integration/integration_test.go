package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

// shortTempDir creates a temp directory with a short path to stay within
// the Unix socket path length limit (104 bytes on macOS).
// t.TempDir() includes the full test name which easily exceeds 104 chars.
// On Windows, named pipes don't have path length limits, so t.TempDir() is fine.
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "cye2e-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		time.Sleep(100 * time.Millisecond) // let async goroutines settle
		os.RemoveAll(dir)
	})
	return dir
}

// setupTestDaemon starts an in-process daemon with a fresh temp dir,
// connects an IPC client, and returns both plus a cleanup function.
func setupTestDaemon(t *testing.T) (*ipc.Client, string, func()) {
	t.Helper()
	baseDir := shortTempDir(t)

	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		shutdown()
		t.Fatalf("Connect: %v", err)
	}

	cleanup := func() {
		client.Disconnect()
		shutdown()
	}
	return client, baseDir, cleanup
}

// callResult is a helper that calls an RPC method and returns the result
// as a map[string]any (most methods return objects), or fatals on error.
func callResult(t *testing.T, client *ipc.Client, method string, params map[string]any) map[string]any {
	t.Helper()
	raw, err := client.Call(method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s: expected map result, got %T", method, raw)
	}
	return m
}

// callList is a helper that calls an RPC method and returns the result
// as a []any (for methods that return arrays).
func callList(t *testing.T, client *ipc.Client, method string, params map[string]any) []any {
	t.Helper()
	raw, err := client.Call(method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s: expected []any result, got %T", method, raw)
	}
	return list
}

// findSession searches a session list (as returned by callList) for a session
// with the given ID and returns it as a map, or nil if not found.
func findSession(list []any, id string) map[string]any {
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if m["id"] == id {
			return m
		}
	}
	return nil
}

// createSession is a convenience helper that creates a native session and
// returns its id.
func createSession(t *testing.T, client *ipc.Client, name string) (id string) {
	t.Helper()
	params := map[string]any{"backend": "native"}
	if name != "" {
		params["name"] = name
	}
	sess := callResult(t, client, "session.create", params)
	id, ok := sess["id"].(string)
	if !ok || id == "" {
		t.Fatal("session.create returned no id")
	}
	return id
}

// ---------------------------------------------------------------------------
// Session lifecycle tests
// ---------------------------------------------------------------------------

func TestSessionCreateAndList(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "test-session-1")
	if !strings.HasPrefix(id, "native-") {
		t.Fatalf("expected id to start with native-, got %s", id)
	}

	sessions := callList(t, client, "session.list", nil)
	found := findSession(sessions, id)
	if found == nil {
		t.Fatalf("session %s not in list", id)
	}
	if found["name"] != "test-session-1" {
		t.Fatalf("expected name test-session-1, got %v", found["name"])
	}
}

func TestSessionCreateCustomName(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	sess := callResult(t, client, "session.create", map[string]any{
		"name":    "my-custom-name",
		"backend": "native",
	})
	if sess["name"] != "my-custom-name" {
		t.Fatalf("expected name my-custom-name, got %v", sess["name"])
	}
}

func TestSessionCreateMultipleAndListAll(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id1 := createSession(t, client, "multi-1")
	id2 := createSession(t, client, "multi-2")
	id3 := createSession(t, client, "multi-3")

	sessions := callList(t, client, "session.list", nil)
	if len(sessions) < 3 {
		t.Fatalf("expected at least 3 sessions, got %d", len(sessions))
	}
	for _, id := range []string{id1, id2, id3} {
		if findSession(sessions, id) == nil {
			t.Fatalf("session %s not in list", id)
		}
	}
}

func TestSessionKill(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "to-kill")
	callResult(t, client, "session.kill", map[string]any{"sessionId": id})

	// Wait for async PTY exit
	time.Sleep(500 * time.Millisecond)

	sessions := callList(t, client, "session.list", nil)
	if findSession(sessions, id) != nil {
		t.Fatal("session should have been removed after kill")
	}
}

func TestSessionRename(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "original-name")
	callResult(t, client, "session.rename", map[string]any{
		"sessionId": id,
		"name":      "renamed-session",
	})

	sessions := callList(t, client, "session.list", nil)
	found := findSession(sessions, id)
	if found == nil {
		t.Fatal("session not found after rename")
	}
	if found["name"] != "renamed-session" {
		t.Fatalf("expected renamed-session, got %v", found["name"])
	}
}

func TestSessionCreateWithCwd(t *testing.T) {
	client, baseDir, cleanup := setupTestDaemon(t)
	defer cleanup()

	customDir := filepath.Join(baseDir, "cwd")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatal(err)
	}

	sess := callResult(t, client, "session.create", map[string]any{
		"name":    "cwd-session",
		"backend": "native",
		"cwd":     customDir,
	})
	if sess["cwd"] != customDir {
		t.Fatalf("expected cwd %s, got %v", customDir, sess["cwd"])
	}

	// Kill session so the process releases the cwd handle before temp dir cleanup (Windows).
	callResult(t, client, "session.kill", map[string]any{"sessionId": sess["id"]})
	time.Sleep(100 * time.Millisecond)
}

func TestSessionCreateWithCommand(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Use a command that echoes then sleeps - gives time to attach and read output
	sess := callResult(t, client, "session.create", map[string]any{
		"name":    "cmd-session",
		"backend": "native",
		"command": "echo hello-command-test; sleep 30",
	})
	if sess["command"] != "echo hello-command-test; sleep 30" {
		t.Fatalf("expected command field set, got %v", sess["command"])
	}

	id := sess["id"].(string)
	defer client.Call("session.kill", map[string]any{"sessionId": id})

	// Poll scrollback until the output appears (avoids attach race)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for command output in scrollback")
		default:
		}
		raw, err := client.Call("session.scrollback", map[string]any{"sessionId": id})
		if err == nil {
			if scrollback, ok := raw.(string); ok && strings.Contains(scrollback, "hello-command-test") {
				return // success
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Attach and I/O tests
// ---------------------------------------------------------------------------

func TestAttachAndEcho(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "echo-test")

	// Attach - returns holder socket for local sessions
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	sockPath, ok := result["holderSocket"].(string)
	if !ok || sockPath == "" {
		t.Fatalf("expected holderSocket, got %v", result)
	}

	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder: %v", err)
	}
	defer hc.Close()

	marker := fmt.Sprintf("e2e-echo-marker-%d", time.Now().UnixNano())
	done := make(chan struct{})
	var output strings.Builder

	hc.OnData(func(data []byte) {
		output.Write(data)
		if strings.Contains(output.String(), marker) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	// Wait for shell to be ready, then send the echo command
	time.Sleep(300 * time.Millisecond)
	hc.Write([]byte(fmt.Sprintf("echo %s\n", marker)))

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for echo output; got so far: %s", output.String())
	}
}

func TestAttachSendsScrollback(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "scrollback-attach")

	// Attach, write a marker via holder, then close
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	sockPath := result["holderSocket"].(string)

	hc1, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder: %v", err)
	}
	marker := fmt.Sprintf("SCROLL_%d", time.Now().UnixNano())
	time.Sleep(300 * time.Millisecond)
	hc1.Write([]byte(fmt.Sprintf("echo %s\n", marker)))
	time.Sleep(500 * time.Millisecond)
	hc1.Close()

	// Second holder connection - scrollback should contain the marker
	hc2, scrollback, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 2: %v", err)
	}
	defer hc2.Close()

	if strings.Contains(string(scrollback), marker) {
		return // success - scrollback contained marker
	}

	// Wait for it in live stream
	done := make(chan struct{})
	var output strings.Builder
	output.Write(scrollback)
	hc2.OnData(func(data []byte) {
		output.Write(data)
		if strings.Contains(output.String(), marker) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatalf("scrollback not received on reconnect. Output so far: %q", output.String())
	}
}

func TestAttachMultipleClients(t *testing.T) {
	client1, _, cleanup1 := setupTestDaemon(t)
	defer cleanup1()

	id := createSession(t, client1, "multi-attach")

	// Get holder socket
	result := callResult(t, client1, "session.attach", map[string]any{"sessionId": id})
	sockPath := result["holderSocket"].(string)

	// Connect two holder clients
	hc1, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 1: %v", err)
	}
	defer hc1.Close()

	hc2, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 2: %v", err)
	}
	defer hc2.Close()

	marker := fmt.Sprintf("multi-client-%d", time.Now().UnixNano())

	var mu sync.Mutex
	var output1, output2 strings.Builder
	done1 := make(chan struct{})
	done2 := make(chan struct{})

	hc1.OnData(func(data []byte) {
		mu.Lock()
		output1.Write(data)
		if strings.Contains(output1.String(), marker) {
			select {
			case <-done1:
			default:
				close(done1)
			}
		}
		mu.Unlock()
	})

	hc2.OnData(func(data []byte) {
		mu.Lock()
		output2.Write(data)
		if strings.Contains(output2.String(), marker) {
			select {
			case <-done2:
			default:
				close(done2)
			}
		}
		mu.Unlock()
	})

	// Send from holder client 1
	time.Sleep(300 * time.Millisecond)
	hc1.Write([]byte(fmt.Sprintf("echo %s\n", marker)))

	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("client1 did not receive echo output")
	}
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("client2 did not receive echo output")
	}
}

func TestDetachSessionContinuesRunning(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "detach-test")

	// Attach and then detach
	callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	stream := client.AttachStream(id)
	stream.Close() // detach

	// Session should still be running
	time.Sleep(200 * time.Millisecond)
	sessions := callList(t, client, "session.list", nil)
	if findSession(sessions, id) == nil {
		t.Fatal("session should still be running after detach")
	}
}

// ---------------------------------------------------------------------------
// Config through daemon tests
// ---------------------------------------------------------------------------

func TestConfigGetDefault(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	raw, err := client.Call("config.get", map[string]any{"key": "local.port"})
	if err != nil {
		t.Fatalf("config.get: %v", err)
	}
	// JSON numbers come back as float64
	port, ok := raw.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T (%v)", raw, raw)
	}
	if int(port) != 8384 {
		t.Fatalf("expected default port 8384, got %v", port)
	}
}

func TestConfigSetAndGet(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	result := callResult(t, client, "config.set", map[string]any{
		"key":   "local.port",
		"value": "9999",
	})
	if result["ok"] != true {
		t.Fatalf("config.set should return ok=true, got %v", result)
	}

	raw, err := client.Call("config.get", map[string]any{"key": "local.port"})
	if err != nil {
		t.Fatalf("config.get: %v", err)
	}
	port, ok := raw.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T", raw)
	}
	if int(port) != 9999 {
		t.Fatalf("expected 9999, got %v", port)
	}
}

func TestConfigReload(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	result := callResult(t, client, "config.reload", nil)
	if result["ok"] != true {
		t.Fatal("config.reload should return ok=true")
	}
}

// ---------------------------------------------------------------------------
// Daemon status tests
// ---------------------------------------------------------------------------

func TestDaemonStatus(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	status := callResult(t, client, "daemon.status", nil)

	pid, ok := status["pid"].(float64)
	if !ok || int(pid) != os.Getpid() {
		t.Fatalf("expected pid %d, got %v", os.Getpid(), status["pid"])
	}

	uptime, ok := status["uptime"].(float64)
	if !ok || uptime <= 0 {
		t.Fatalf("expected positive uptime, got %v", status["uptime"])
	}

	sessions, ok := status["sessions"].(float64)
	if !ok || sessions < 0 {
		t.Fatalf("expected sessions >= 0, got %v", status["sessions"])
	}

	backends, ok := status["backends"].([]any)
	if !ok || len(backends) == 0 {
		t.Fatalf("expected backends list, got %v", status["backends"])
	}

	// Verify native backend is present
	found := false
	for _, b := range backends {
		bm, ok := b.(map[string]any)
		if ok && bm["id"] == "native" && bm["available"] == true {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("native backend not found in status backends")
	}

	// Verify local status is present
	local, ok := status["local"].(map[string]any)
	if !ok {
		t.Fatal("expected 'local' field in daemon.status")
	}
	if _, ok := local["enabled"].(bool); !ok {
		t.Fatal("expected 'enabled' in local status")
	}
	if _, ok := local["expose"].(bool); !ok {
		t.Fatal("expected 'expose' in local status")
	}

	// Verify remote status is present
	remote, ok := status["remote"].(map[string]any)
	if !ok {
		t.Fatal("expected 'remote' field in daemon.status")
	}
	if _, ok := remote["enabled"].(bool); !ok {
		t.Fatal("expected 'enabled' in remote status")
	}
}

// ---------------------------------------------------------------------------
// Scrollback test
// ---------------------------------------------------------------------------

func TestScrollback(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "scrollback-test")

	// Get holder socket
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	sockPath := result["holderSocket"].(string)

	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder: %v", err)
	}

	marker := fmt.Sprintf("scrollback-marker-%d", time.Now().UnixNano())
	done := make(chan struct{})
	var output strings.Builder

	hc.OnData(func(data []byte) {
		output.Write(data)
		if strings.Contains(output.String(), marker) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	time.Sleep(300 * time.Millisecond)
	hc.Write([]byte(fmt.Sprintf("echo %s\n", marker)))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for echo")
	}
	hc.Close()

	// Now get scrollback via daemon - it should contain the marker
	raw, err := client.Call("session.scrollback", map[string]any{"sessionId": id})
	if err != nil {
		t.Fatalf("session.scrollback: %v", err)
	}
	scrollback, _ := raw.(string)
	if !strings.Contains(scrollback, marker) {
		t.Fatalf("scrollback should contain %s, got: %s", marker, scrollback)
	}
}

// ---------------------------------------------------------------------------
// Project integration tests
// ---------------------------------------------------------------------------

func TestProjectAssociateAndTerminals(t *testing.T) {
	client, baseDir, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "project-session")

	projectPath := filepath.Join(baseDir, "proj")
	if err := os.MkdirAll(projectPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Associate
	assocResult := callResult(t, client, "project.associate", map[string]any{
		"path":      projectPath,
		"sessionId": id,
	})
	if assocResult["ok"] != true {
		t.Fatal("project.associate should return ok=true")
	}

	// Get terminals
	termResult := callResult(t, client, "project.terminals", map[string]any{
		"path": projectPath,
	})

	associated, ok := termResult["associated"].([]any)
	if !ok {
		t.Fatalf("expected associated to be array, got %T", termResult["associated"])
	}
	if findSession(associated, id) == nil {
		t.Fatal("associated session not found in project.terminals result")
	}
}

// ---------------------------------------------------------------------------
// Daemon lifecycle tests
// ---------------------------------------------------------------------------

func TestDaemonSocketExists(t *testing.T) {
	_, baseDir, cleanup := setupTestDaemon(t)
	defer cleanup()

	// On Unix, the socket is a file on disk. On Windows, it's a named pipe.
	if runtime.GOOS != "windows" {
		socketPath := daemon.GetSocketPath(baseDir)
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			t.Fatal("socket file should exist")
		}
	}

	pidPath := filepath.Join(baseDir, "daemon.pid")
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Fatal("PID file should exist")
	}
}

func TestDaemonShutdownCleansUp(t *testing.T) {
	baseDir := shortTempDir(t)

	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	pidPath := filepath.Join(baseDir, "daemon.pid")

	// Socket file exists before shutdown (Unix only)
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			t.Fatal("socket should exist before shutdown")
		}
	}
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Fatal("PID file should exist before shutdown")
	}

	shutdown()
	time.Sleep(100 * time.Millisecond)

	// Socket file removed after shutdown (Unix only)
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
			t.Fatal("socket should be removed after shutdown")
		}
	}
	_ = socketPath // used above conditionally
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("PID file should be removed after shutdown")
	}
}

func TestEnsureDaemonDetectsRunning(t *testing.T) {
	_, baseDir, cleanup := setupTestDaemon(t)
	defer cleanup()

	// EnsureDaemon should detect the already-running daemon and return quickly
	start := time.Now()
	err := daemon.EnsureDaemon(baseDir)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("EnsureDaemon took too long (%v); should detect running daemon quickly", elapsed)
	}

	// Socket should still exist (Unix only)
	if runtime.GOOS != "windows" {
		socketPath := daemon.GetSocketPath(baseDir)
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			t.Fatal("socket should still exist after EnsureDaemon")
		}
	}
}

// ---------------------------------------------------------------------------
// Localhost autostart tests
// ---------------------------------------------------------------------------

func TestLocalEnableAndDisable(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Set port to something high and unique to avoid conflicts
	port := 18900 + os.Getpid()%100
	callResult(t, client, "config.set", map[string]any{
		"key":   "local.port",
		"value": fmt.Sprintf("%d", port),
	})
	callResult(t, client, "config.set", map[string]any{
		"key":   "local.expose",
		"value": "false",
	})

	// Enable local server via config.set (local.enable RPC was removed)
	enableResult := callResult(t, client, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "true",
	})
	if enableResult["ok"] != true {
		t.Fatal("config.set local.enabled=true should return ok=true")
	}

	// Verify config was persisted
	raw, err := client.Call("config.get", map[string]any{"key": "local.enabled"})
	if err != nil {
		t.Fatalf("config.get: %v", err)
	}
	if raw != true {
		t.Fatalf("expected local.enabled=true, got %v", raw)
	}

	// Give it a moment to start
	time.Sleep(500 * time.Millisecond)

	// Test HTTP
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "carryOn") {
		t.Fatal("expected HTML to contain carryOn")
	}

	// Test sessions API
	sessResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port))
	if err != nil {
		t.Fatalf("sessions API: %v", err)
	}
	defer sessResp.Body.Close()
	if sessResp.StatusCode != 200 {
		t.Fatalf("expected sessions API status 200, got %d", sessResp.StatusCode)
	}
	var sessions []any
	if err := json.NewDecoder(sessResp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}

	// Get local server status via daemon.status (local.status RPC was removed)
	daemonResult := callResult(t, client, "daemon.status", nil)
	localField, ok := daemonResult["local"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'local' field in daemon.status, got %v", daemonResult)
	}
	running, ok := localField["running"].(bool)
	if !ok || !running {
		t.Fatalf("expected local server running=true, got %v", localField)
	}

	// Disable local server via config.set (local.disable RPC was removed)
	disableResult := callResult(t, client, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "false",
	})
	if disableResult["ok"] != true {
		t.Fatal("config.set local.enabled=false should return ok=true")
	}

	time.Sleep(300 * time.Millisecond)

	// Verify config was persisted
	raw, err = client.Call("config.get", map[string]any{"key": "local.enabled"})
	if err != nil {
		t.Fatalf("config.get: %v", err)
	}
	if raw != false {
		t.Fatalf("expected local.enabled=false, got %v", raw)
	}

	// HTTP should fail now
	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err == nil {
		t.Fatal("expected connection refused after disable")
	}
}

func TestLocalStatus(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Before enabling, daemon.status should show local server not running
	// (local.status RPC was removed; use daemon.status instead)
	daemonResult := callResult(t, client, "daemon.status", nil)
	localField, ok := daemonResult["local"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'local' field in daemon.status, got %v", daemonResult)
	}
	running, ok := localField["running"].(bool)
	if !ok || running {
		t.Fatalf("expected local server running=false before enable, got %v", localField)
	}
}

func TestLocalAutostart(t *testing.T) {
	baseDir := shortTempDir(t)

	// Pre-configure local.enabled = true before starting daemon
	port := 18800 + os.Getpid()%100
	cfg := config.NewManager(baseDir)
	cfg.Set("local.enabled", "true")
	cfg.Set("local.port", fmt.Sprintf("%d", port))
	cfg.Set("local.expose", "false")

	// Start daemon -- it should auto-start local server
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	// Give it a moment
	time.Sleep(500 * time.Millisecond)

	// Web server should be running without calling local.enable
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("auto-started local server should be reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "carryOn") {
		t.Fatal("expected HTML to contain carryOn")
	}
}

// ---------------------------------------------------------------------------
// Daemon logs test
// ---------------------------------------------------------------------------

func TestDaemonLogs(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	result := callResult(t, client, "daemon.logs", map[string]any{"last": float64(10)})
	entries, ok := result["entries"].([]any)
	if !ok {
		t.Fatalf("expected entries array, got %T", result["entries"])
	}
	// Daemon startup should produce some log entries
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry from daemon startup")
	}
}

func TestDaemonLogsFollow(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	result := callResult(t, client, "daemon.logs", map[string]any{
		"last":   float64(5),
		"follow": true,
	})
	subID, ok := result["subscriptionId"].(string)
	if !ok || subID == "" {
		t.Fatal("expected subscriptionId in follow response")
	}

	// Cancel the subscription
	cancelResult := callResult(t, client, "subscribe.cancel", map[string]any{
		"subscriptionId": subID,
	})
	if cancelResult["ok"] != true {
		t.Fatal("subscribe.cancel should return ok=true")
	}
}

// ---------------------------------------------------------------------------
// Session resize test
// ---------------------------------------------------------------------------

func TestSessionResize(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "resize-test")

	result := callResult(t, client, "session.resize", map[string]any{
		"sessionId": id,
		"cols":      float64(120),
		"rows":      float64(40),
	})
	if result["ok"] != true {
		t.Fatal("session.resize should return ok=true")
	}
}

// ---------------------------------------------------------------------------
// Config edge cases
// ---------------------------------------------------------------------------

func TestConfigSetInvalidValue(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	_, err := client.Call("config.set", map[string]any{
		"key":   "local.port",
		"value": "not-a-number",
	})
	if err == nil {
		t.Fatal("expected error for invalid config value")
	}
}

func TestConfigSetWarnOnExpose(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	result := callResult(t, client, "config.set", map[string]any{
		"key":   "local.expose",
		"value": "true",
	})
	if result["ok"] != true {
		t.Fatal("config.set should return ok=true for local.expose=true")
	}
	warning, ok := result["warning"].(string)
	if !ok || warning == "" {
		t.Fatalf("expected warning about expose, got %v", result["warning"])
	}
}

// ---------------------------------------------------------------------------
// Expose + password flow tests
// ---------------------------------------------------------------------------

func TestExposeAutoGeneratesPassword(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Enable the local server first
	callResult(t, client, "config.set", map[string]any{
		"key":   "local.enabled",
		"value": "true",
	})
	time.Sleep(200 * time.Millisecond)

	// Enable expose - should auto-generate password
	result := callResult(t, client, "config.set", map[string]any{
		"key":   "local.expose",
		"value": "true",
	})

	// Should have both the expose warning and the generated password
	warning, _ := result["warning"].(string)
	if !strings.Contains(warning, "Exposing") {
		t.Fatalf("expected expose warning, got: %s", warning)
	}
	pw, ok := result["generated_password"].(string)
	if !ok || pw == "" {
		t.Fatalf("expected generated_password in response, got: %v", result)
	}
	if len(pw) != 16 {
		t.Fatalf("expected 16-char password, got %d chars: %s", len(pw), pw)
	}

	// Verify password hash was stored in config
	pwResult, err := client.Call("config.get", map[string]any{"key": "local.password"})
	if err != nil {
		t.Fatalf("config.get local.password: %v", err)
	}
	hashStr := fmt.Sprintf("%v", pwResult)
	if !strings.HasPrefix(hashStr, "$2a$") {
		t.Fatalf("expected bcrypt hash in local.password, got: %v", pwResult)
	}
}

func TestSetPasswordRPC(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Set password via RPC
	result := callResult(t, client, "local.set-password", map[string]any{
		"password": "mysecurepassword",
	})
	if result["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", result)
	}

	// Verify hash was stored
	pwResult, err := client.Call("config.get", map[string]any{"key": "local.password"})
	if err != nil {
		t.Fatalf("config.get local.password: %v", err)
	}
	hashStr := fmt.Sprintf("%v", pwResult)
	if !strings.HasPrefix(hashStr, "$2a$") {
		t.Fatalf("expected bcrypt hash, got: %v", pwResult)
	}

	// Too short should fail
	_, err = client.Call("local.set-password", map[string]any{
		"password": "short",
	})
	if err == nil {
		t.Fatal("expected error for short password")
	}
}

func TestProjectDisassociate(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Disconnect()

	// Create a session
	sess, err := client.Call("session.create", map[string]any{"name": "disassoc-test"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	projectPath := filepath.Join(os.TempDir(), "test-disassoc-project")

	// Associate
	_, err = client.Call("project.associate", map[string]any{
		"path":      projectPath,
		"sessionId": sessionID,
	})
	if err != nil {
		t.Fatalf("associate: %v", err)
	}

	// Verify associated
	result, err := client.Call("project.terminals", map[string]any{"path": projectPath})
	if err != nil {
		t.Fatalf("terminals: %v", err)
	}
	rm := result.(map[string]any)
	associated, _ := rm["associated"].([]any)
	if len(associated) != 1 {
		t.Fatalf("expected 1 associated session, got %d", len(associated))
	}

	// Disassociate
	_, err = client.Call("project.disassociate", map[string]any{
		"path":      projectPath,
		"sessionId": sessionID,
	})
	if err != nil {
		t.Fatalf("disassociate: %v", err)
	}

	// Verify disassociated
	result, err = client.Call("project.terminals", map[string]any{"path": projectPath})
	if err != nil {
		t.Fatalf("terminals after disassociate: %v", err)
	}
	rm = result.(map[string]any)
	associated, _ = rm["associated"].([]any)
	if len(associated) != 0 {
		t.Fatalf("expected 0 associated sessions after disassociate, got %d", len(associated))
	}
}

// ---------------------------------------------------------------------------
// Direct-to-holder attach tests - sessions survive daemon stop
// ---------------------------------------------------------------------------

func TestDirectAttachSurvivesDaemonStop(t *testing.T) {
	// This test proves that a direct holder client connection keeps working
	// after the daemon IPC layer is torn down. In production, holders are
	// separate processes so they survive a full daemon stop. In the in-process
	// test setup, we simulate this by spawning a holder directly and then
	// verifying that the daemon's IPC shutdown does not affect it.
	baseDir := shortTempDir(t)

	// Spawn a holder directly - this simulates the production case where
	// the holder is an independent process.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	h, err := holder.Spawn(holder.SpawnOpts{
		SessionID: "native-survive-test",
		Shell:     shell,
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("holder.Spawn: %v", err)
	}
	defer h.Shutdown()

	sockPath := holder.SocketPath(baseDir, "native-survive-test")

	// Start a daemon using the same baseDir
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		shutdown()
		t.Fatalf("Connect: %v", err)
	}

	// Connect directly to the holder
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		client.Disconnect()
		shutdown()
		t.Fatalf("ConnectHolder: %v", err)
	}
	defer hc.Close()

	var mu sync.Mutex
	var output strings.Builder
	done := make(chan struct{})

	hc.OnData(func(data []byte) {
		mu.Lock()
		output.Write(data)
		if strings.Contains(output.String(), "DAEMON_STOPPED_MARKER_55") {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		mu.Unlock()
	})

	// Wait for shell to be ready
	time.Sleep(300 * time.Millisecond)

	// Stop the daemon (IPC server, session manager, etc.)
	client.Disconnect()
	shutdown()

	// Wait for daemon shutdown to settle
	time.Sleep(200 * time.Millisecond)

	// Write to the holder client - the holder is still alive because it was
	// spawned independently, just like a separate-process holder in production.
	if err := hc.Write([]byte("echo DAEMON_STOPPED_MARKER_55\n")); err != nil {
		t.Fatalf("Write after daemon stop: %v", err)
	}

	// Poll for the marker in captured output
	select {
	case <-done:
		// Success - session survived daemon stop
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := output.String()
		mu.Unlock()
		t.Fatalf("timed out waiting for marker after daemon stop; output so far: %s", got)
	}
}

func TestMultiClientFanOutViaIntegration(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "fanout-test")

	// Attach - get holder socket path
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	sockPath, ok := result["holderSocket"].(string)
	if !ok || sockPath == "" {
		t.Fatalf("expected holderSocket, got %v", result)
	}

	// Connect two holder clients
	hc1, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 1: %v", err)
	}
	defer hc1.Close()

	hc2, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 2: %v", err)
	}
	defer hc2.Close()

	var mu sync.Mutex
	var output1, output2 strings.Builder
	done1 := make(chan struct{})
	done2 := make(chan struct{})

	hc1.OnData(func(data []byte) {
		mu.Lock()
		output1.Write(data)
		if strings.Contains(output1.String(), "FANOUT_MARKER_88") {
			select {
			case <-done1:
			default:
				close(done1)
			}
		}
		mu.Unlock()
	})

	hc2.OnData(func(data []byte) {
		mu.Lock()
		output2.Write(data)
		if strings.Contains(output2.String(), "FANOUT_MARKER_88") {
			select {
			case <-done2:
			default:
				close(done2)
			}
		}
		mu.Unlock()
	})

	// Wait for shell to be ready
	time.Sleep(300 * time.Millisecond)

	// Write from client 1
	if err := hc1.Write([]byte("echo FANOUT_MARKER_88\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Both clients should see the marker
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := output1.String()
		mu.Unlock()
		t.Fatalf("client1 did not receive marker; output so far: %s", got)
	}

	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := output2.String()
		mu.Unlock()
		t.Fatalf("client2 did not receive marker; output so far: %s", got)
	}
}

func TestSessionEndNotifiesAllClients(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	// Create a session with a short-lived command
	sess := callResult(t, client, "session.create", map[string]any{
		"name":    "exit-notify-test",
		"backend": "native",
		"command": "sleep 1 && exit 0",
	})
	id, ok := sess["id"].(string)
	if !ok || id == "" {
		t.Fatal("session.create returned no id")
	}

	// Attach - get holder socket path
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	sockPath, ok := result["holderSocket"].(string)
	if !ok || sockPath == "" {
		t.Fatalf("expected holderSocket, got %v", result)
	}

	// Connect two holder clients
	hc1, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 1: %v", err)
	}
	defer hc1.Close()

	hc2, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder 2: %v", err)
	}
	defer hc2.Close()

	var mu sync.Mutex
	exit1 := make(chan int32, 1)
	exit2 := make(chan int32, 1)

	hc1.OnExit(func(code int32) {
		mu.Lock()
		select {
		case exit1 <- code:
		default:
		}
		mu.Unlock()
	})

	hc2.OnExit(func(code int32) {
		mu.Lock()
		select {
		case exit2 <- code:
		default:
		}
		mu.Unlock()
	})

	// Wait for both clients to receive exit notification
	var code1, code2 int32
	select {
	case code1 = <-exit1:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for exit notification on client 1")
	}

	select {
	case code2 = <-exit2:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for exit notification on client 2")
	}

	if code1 != 0 {
		t.Fatalf("client 1 expected exit code 0, got %d", code1)
	}
	if code2 != 0 {
		t.Fatalf("client 2 expected exit code 0, got %d", code2)
	}
}

// ---------------------------------------------------------------------------
// Direct session attachment tracking tests
// ---------------------------------------------------------------------------

// TestDirectAttachShowsInSessionClients verifies that after a client calls
// session.attach for a local session, the session appears in session.list
// with one IPC client tracked in its clients array.
func TestDirectAttachShowsInSessionClients(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	id := createSession(t, client, "direct-attach-clients-test")

	// Call session.attach - this registers the direct session on the IPC client.
	result := callResult(t, client, "session.attach", map[string]any{"sessionId": id})
	if _, ok := result["holderSocket"].(string); !ok {
		t.Fatalf("expected holderSocket in attach result, got %v", result)
	}

	// Give PostRPC a moment to execute.
	time.Sleep(50 * time.Millisecond)

	// session.list should show this client in the session's clients array.
	sessions := callList(t, client, "session.list", nil)
	found := findSession(sessions, id)
	if found == nil {
		t.Fatalf("session %s not found in session.list", id)
	}

	clients, ok := found["clients"].([]any)
	if !ok {
		t.Fatalf("expected clients to be []any, got %T (%v)", found["clients"], found["clients"])
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 attached IPC client, got %d", len(clients))
	}
}

// TestDirectAttachDetachBroadcast verifies that session.attached and
// session.detached notifications are broadcast when a client calls
// session.attach and then disconnects.
func TestDirectAttachDetachBroadcast(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Client 1 (observer) subscribes to session events.
	observer := ipc.NewClient()
	if err := observer.Connect(socketPath); err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	defer observer.Disconnect()

	gotAttached := make(chan map[string]any, 1)
	gotDetached := make(chan map[string]any, 1)

	observer.OnNotification("session.attached", func(params map[string]any) {
		select {
		case gotAttached <- params:
		default:
		}
	})
	observer.OnNotification("session.detached", func(params map[string]any) {
		select {
		case gotDetached <- params:
		default:
		}
	})

	// Client 2 creates a session and calls session.attach.
	actor := ipc.NewClient()
	if err := actor.Connect(socketPath); err != nil {
		t.Fatalf("connect actor: %v", err)
	}

	sess, err := actor.Call("session.create", map[string]any{"name": "direct-attach-broadcast-test"})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	sessionID := sess.(map[string]any)["id"].(string)

	_, err = actor.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("session.attach: %v", err)
	}

	// Observer should receive session.attached.
	select {
	case params := <-gotAttached:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s in attached event, got %v", sessionID, params["sessionId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.attached broadcast")
	}

	// Disconnect client 2 - cleanup should broadcast session.detached.
	actor.Disconnect()

	// Observer should receive session.detached.
	select {
	case params := <-gotDetached:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s in detached event, got %v", sessionID, params["sessionId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.detached broadcast")
	}

	// Clean up the session.
	cleaner := ipc.NewClient()
	if err := cleaner.Connect(socketPath); err == nil {
		cleaner.Call("session.kill", map[string]any{"sessionId": sessionID})
		cleaner.Disconnect()
	}
}

// TestMultipleDirectAttachSameSession verifies that when two clients both call
// session.attach on the same session the clients array reflects both, and
// drops back to one after one client disconnects.
func TestMultipleDirectAttachSameSession(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Connect two independent clients.
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		t.Fatalf("connect client1: %v", err)
	}
	defer client1.Disconnect()

	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("connect client2: %v", err)
	}

	// Create the session via client1.
	sess, err := client1.Call("session.create", map[string]any{"name": "multi-direct-attach-test"})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	sessionID := sess.(map[string]any)["id"].(string)

	// Both clients attach.
	if _, err := client1.Call("session.attach", map[string]any{"sessionId": sessionID}); err != nil {
		t.Fatalf("client1 session.attach: %v", err)
	}
	if _, err := client2.Call("session.attach", map[string]any{"sessionId": sessionID}); err != nil {
		t.Fatalf("client2 session.attach: %v", err)
	}

	// Give PostRPC a moment to execute for both calls.
	time.Sleep(50 * time.Millisecond)

	// A third client queries the list to avoid counting itself.
	lister := ipc.NewClient()
	if err := lister.Connect(socketPath); err != nil {
		t.Fatalf("connect lister: %v", err)
	}
	defer lister.Disconnect()

	// Both clients should appear in the session's clients array.
	sessions := callList(t, lister, "session.list", nil)
	found := findSession(sessions, sessionID)
	if found == nil {
		t.Fatalf("session %s not found in session.list", sessionID)
	}
	clients, ok := found["clients"].([]any)
	if !ok {
		t.Fatalf("expected clients to be []any, got %T (%v)", found["clients"], found["clients"])
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 attached IPC clients, got %d", len(clients))
	}

	// Disconnect one client - its direct session should be removed.
	client2.Disconnect()
	time.Sleep(100 * time.Millisecond)

	// Now only one client should remain.
	sessions = callList(t, lister, "session.list", nil)
	found = findSession(sessions, sessionID)
	if found == nil {
		t.Fatalf("session %s not found in session.list after disconnect", sessionID)
	}
	clients, ok = found["clients"].([]any)
	if !ok {
		t.Fatalf("expected clients to be []any after disconnect, got %T (%v)", found["clients"], found["clients"])
	}
	if len(clients) != 1 {
		t.Fatalf("expected 1 attached IPC client after disconnect, got %d", len(clients))
	}
}
