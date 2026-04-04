//go:build !windows

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

// buildBinary builds the carryon binary for recovery tests that need
// detached holder processes (SpawnProcess re-execs os.Executable).
func buildBinary(t *testing.T) string {
	t.Helper()
	binPath := t.TempDir() + "/carryon"
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = findProjectRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build carryon: %v\n%s", err, out)
	}
	return binPath
}

// findProjectRoot walks up from the test file to find go.mod.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			t.Fatal("could not find project root")
		}
		dir = parent
	}
}

func TestDaemonRestartSessionPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery test in short mode")
	}

	binPath := buildBinary(t)
	baseDir := shortTempDir(t)
	marker := fmt.Sprintf("PERSIST_%d", time.Now().UnixNano())

	// --- Phase 1: Start daemon with detached holders, create session ---
	shutdown1, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		shutdown1()
		t.Fatalf("Connect: %v", err)
	}

	sess, err := client1.Call("session.create", map[string]any{"name": "persist-test"})
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("session.create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// Attach and write marker via holder
	attachResult, err := client1.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("session.attach: %v", err)
	}
	attachMap := attachResult.(map[string]any)
	sockPath := attachMap["holderSocket"].(string)

	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("ConnectHolder: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	hc.Write([]byte(fmt.Sprintf("echo %s\n", marker)))
	time.Sleep(1 * time.Second)

	// --- Phase 2: Stop daemon (holders survive as separate processes) ---
	hc.Close()
	client1.Disconnect()
	shutdown1()
	time.Sleep(500 * time.Millisecond)

	// Verify holder socket still exists
	holderSock := baseDir + "/holders/" + sessionID + ".sock"
	if _, err := os.Stat(holderSock); err != nil {
		t.Fatalf("holder socket gone after daemon stop: %v (path: %s)", err, holderSock)
	}
	t.Logf("holder socket exists: %s", holderSock)

	// Check sessions.json
	stateFile := baseDir + "/state/sessions.json"
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Logf("sessions.json read error: %v", err)
	} else {
		t.Logf("sessions.json: %s", string(data))
	}

	// --- Phase 3: Restart daemon, verify session recovered ---
	shutdown2, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon (restart): %v", err)
	}
	defer shutdown2()

	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("Connect (restart): %v", err)
	}
	defer client2.Disconnect()

	// Session should be in the list
	sessions, err := client2.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	found := false
	for _, s := range list {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("session %s not found after restart. Sessions: %v", sessionID, list)
	}

	// Scrollback should contain the marker
	sb, err := client2.Call("session.scrollback", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("session.scrollback: %v", err)
	}
	content := fmt.Sprintf("%v", sb)
	if !strings.Contains(content, marker) {
		t.Fatalf("scrollback missing marker %q. Got: %q", marker, content)
	}

	// Session should be alive - can interact via holder
	attachResult2, err := client2.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("session.attach (after restart): %v", err)
	}
	sockPath2 := attachResult2.(map[string]any)["holderSocket"].(string)

	hc2, _, err := holder.ConnectHolder(sockPath2)
	if err != nil {
		t.Fatalf("ConnectHolder (after restart): %v", err)
	}
	defer hc2.Close()

	marker2 := fmt.Sprintf("ALIVE_%d", time.Now().UnixNano())
	done := make(chan struct{})
	var output strings.Builder
	hc2.OnData(func(data []byte) {
		output.Write(data)
		if strings.Contains(output.String(), marker2) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	time.Sleep(300 * time.Millisecond)
	hc2.Write([]byte(fmt.Sprintf("echo %s\n", marker2)))

	select {
	case <-done:
		// Session alive and interactive after restart
	case <-time.After(5 * time.Second):
		t.Fatalf("session not responsive after restart. Output: %s", output.String())
	}

	client2.Call("session.kill", map[string]any{"sessionId": sessionID})
}

func TestDaemonRestartMultipleSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery test in short mode")
	}

	binPath := buildBinary(t)

	baseDir := shortTempDir(t)

	shutdown1, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		shutdown1()
		t.Fatalf("Connect: %v", err)
	}

	var ids []string
	for i := 0; i < 3; i++ {
		sess, err := client1.Call("session.create", map[string]any{
			"name": fmt.Sprintf("session-%d", i),
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		sm := sess.(map[string]any)
		ids = append(ids, sm["id"].(string))
	}

	client1.Disconnect()
	shutdown1()
	time.Sleep(500 * time.Millisecond)

	shutdown2, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon (restart): %v", err)
	}
	defer shutdown2()

	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("Connect (restart): %v", err)
	}
	defer client2.Disconnect()

	sessions, err := client2.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	recovered := make(map[string]bool)
	for _, s := range list {
		m := s.(map[string]any)
		recovered[m["id"].(string)] = true
	}
	for _, id := range ids {
		if !recovered[id] {
			t.Fatalf("session %s not recovered", id)
		}
	}

	for _, id := range ids {
		client2.Call("session.kill", map[string]any{"sessionId": id})
	}
}

func TestDaemonRecoveryRebuildsClientCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery test in short mode")
	}

	binPath := buildBinary(t)
	baseDir := shortTempDir(t)

	// --- Phase 1: Start daemon, create session, connect a holder client ---
	shutdown1, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		shutdown1()
		t.Fatalf("Connect: %v", err)
	}

	sess, err := client1.Call("session.create", map[string]any{"name": "rebuild-test"})
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("session.create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// Attach to get the holder socket path.
	attachResult, err := client1.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("session.attach: %v", err)
	}
	attachMap := attachResult.(map[string]any)
	holderSocket := attachMap["holderSocket"].(string)

	// Connect directly to the holder.
	hc, _, err := holder.ConnectHolder(holderSocket)
	if err != nil {
		client1.Disconnect()
		shutdown1()
		t.Fatalf("ConnectHolder: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Verify the holder client works - write a command.
	if err := hc.Write([]byte("echo hello\n")); err != nil {
		hc.Close()
		client1.Disconnect()
		shutdown1()
		t.Fatalf("Write to holder: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// --- Phase 2: Stop daemon (holder client and holder process survive) ---
	client1.Disconnect()
	shutdown1()
	time.Sleep(500 * time.Millisecond)

	// Holder client is still connected - verify it still works.
	if err := hc.Write([]byte("echo still_alive\n")); err != nil {
		t.Logf("Note: holder write after daemon stop: %v", err)
	}

	// --- Phase 3: Restart daemon, verify session recovered ---
	shutdown2, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		hc.Close()
		t.Fatalf("StartDaemon (restart): %v", err)
	}
	defer shutdown2()
	defer hc.Close()

	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("Connect (restart): %v", err)
	}
	defer client2.Disconnect()

	// Session should be in the list after recovery with the correct client count.
	sessions, err := client2.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	var foundSession map[string]any
	for _, s := range list {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			foundSession = m
		}
	}
	if foundSession == nil {
		t.Fatalf("session %s not found after restart. Sessions: %v", sessionID, list)
	}

	// The holder client (hc) is still connected to the holder process.
	// Recovery queries the holder for status and subtracts 1 for its own
	// probe connection, so the stored count should be 1 (the real client).
	attachedClients, ok := foundSession["attachedClients"]
	if !ok {
		t.Fatalf("session missing attachedClients field: %v", foundSession)
	}
	// JSON numbers from IPC are float64.
	clientCount := int(attachedClients.(float64))
	if clientCount != 1 {
		t.Errorf("expected attachedClients=1 after recovery, got %d", clientCount)
	}

	client2.Call("session.kill", map[string]any{"sessionId": sessionID})
}

func TestDaemonRestartDeadSessionCleaned(t *testing.T) {
	// This test uses InProcess mode - no binary build needed.
	// The session exits immediately, the in-process holder dies, and on
	// restart the socket is gone so recovery cleans it up.
	baseDir := shortTempDir(t)

	shutdown1, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}

	socketPath := daemon.GetSocketPath(baseDir)
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		shutdown1()
		t.Fatalf("Connect: %v", err)
	}

	sess, err := client1.Call("session.create", map[string]any{
		"name":    "dies-fast",
		"command": "exit 0",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	time.Sleep(1 * time.Second)

	client1.Disconnect()
	shutdown1()
	time.Sleep(500 * time.Millisecond)

	shutdown2, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon (restart): %v", err)
	}
	defer shutdown2()

	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("Connect (restart): %v", err)
	}
	defer client2.Disconnect()

	sessions, err := client2.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	for _, s := range list {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			t.Fatalf("dead session %s should have been cleaned up", sessionID)
		}
	}
}
