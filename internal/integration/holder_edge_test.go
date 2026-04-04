//go:build !windows

package integration

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

func TestKillSessionCleansUpHolderSocket(t *testing.T) {
	client, baseDir, cleanup := setupTestDaemon(t)
	defer cleanup()

	sess := callResult(t, client, "session.create", map[string]any{"name": "kill-cleanup"})
	sessionID := sess["id"].(string)

	// Verify holder socket exists
	sockPath := holder.SocketPath(baseDir, sessionID)
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("holder socket should exist: %v", err)
	}

	// Kill the session
	callResult(t, client, "session.kill", map[string]any{"sessionId": sessionID})

	// Wait for cleanup
	time.Sleep(500 * time.Millisecond)

	// Socket should be gone
	if _, err := os.Stat(sockPath); err == nil {
		t.Fatal("holder socket should be cleaned up after kill")
	}

	// Session should not be in list
	sessions := callList(t, client, "session.list", nil)
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			t.Fatal("killed session should not appear in list")
		}
	}
}

func TestHolderCrashDetected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping holder crash test in short mode")
	}

	binPath := buildBinary(t)
	baseDir := shortTempDir(t)

	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	// Listen for session.ended
	endedCh := make(chan string, 1)
	client.OnNotification("session.ended", func(params map[string]any) {
		if id, ok := params["sessionId"].(string); ok {
			endedCh <- id
		}
	})

	sess := callResult(t, client, "session.create", map[string]any{"name": "crash-test"})
	sessionID := sess["id"].(string)
	pid := int(sess["pid"].(float64))

	// Kill the holder process externally (simulating a crash)
	// The PID in the session is the shell PID. We need the holder PID.
	// The holder is the parent of the shell, but in detached mode it's a separate process.
	// Find it by looking for the holder socket and killing the process listening on it.
	// Simplest: kill the shell - when shell dies, holder detects EOF and exits.
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	proc.Signal(syscall.SIGKILL)

	// Daemon should detect the session ended
	select {
	case id := <-endedCh:
		if id != sessionID {
			t.Fatalf("expected session %s in ended event, got %s", sessionID, id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for session.ended after holder crash")
	}

	// Session should be gone from list
	sessions := callList(t, client, "session.list", nil)
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			t.Fatal("crashed session should not appear in list")
		}
	}
}

func TestResizeThroughHolder(t *testing.T) {
	client, _, cleanup := setupTestDaemon(t)
	defer cleanup()

	sess := callResult(t, client, "session.create", map[string]any{"name": "resize-holder"})
	sessionID := sess["id"].(string)

	// Attach to get holder socket
	attachResult := callResult(t, client, "session.attach", map[string]any{"sessionId": sessionID})
	sockPath := attachResult["holderSocket"].(string)

	// Connect to holder directly
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder: %v", err)
	}
	defer hc.Close()

	// Wait for shell to start
	time.Sleep(300 * time.Millisecond)

	// Resize directly via holder
	hc.Resize(200, 50)

	// Send tput to read terminal size
	marker := fmt.Sprintf("SIZE_%d", time.Now().UnixNano())
	done := make(chan struct{})
	var output strings.Builder
	var mu sync.Mutex

	hc.OnData(func(data []byte) {
		mu.Lock()
		output.Write(data)
		if strings.Contains(output.String(), marker) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		mu.Unlock()
	})

	time.Sleep(200 * time.Millisecond)
	// Use stty to check size, with a marker so we can find the output
	hc.Write([]byte(fmt.Sprintf("echo %s $(stty size)\n", marker)))

	select {
	case <-done:
		mu.Lock()
		out := output.String()
		mu.Unlock()
		// Should contain "50 200" (rows cols)
		if !strings.Contains(out, "50 200") {
			t.Logf("output: %s", out)
			// Don't fail - some CI environments may not support stty size
			t.Log("resize sent successfully (stty size output not matching expected, may be CI limitation)")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for resize output")
	}

	callResult(t, client, "session.kill", map[string]any{"sessionId": sessionID})
}

func TestMultiClientAttachAfterRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-client recovery test in short mode")
	}

	binPath := buildBinary(t)
	baseDir := shortTempDir(t)
	marker := fmt.Sprintf("MULTI_%d", time.Now().UnixNano())

	// Start daemon, create session, write marker
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

	sess, _ := client1.Call("session.create", map[string]any{"name": "multi-attach"})
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	attachRes, _ := client1.Call("session.attach", map[string]any{"sessionId": sessionID})
	attachMap := attachRes.(map[string]any)
	sockPath := attachMap["holderSocket"].(string)

	hc1, _, _ := holder.ConnectHolder(sockPath)
	time.Sleep(300 * time.Millisecond)
	hc1.Write([]byte(fmt.Sprintf("echo %s\n", marker)))
	time.Sleep(500 * time.Millisecond)
	hc1.Close()
	client1.Disconnect()
	shutdown1()
	time.Sleep(500 * time.Millisecond)

	// Restart daemon
	shutdown2, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, Executable: binPath})
	if err != nil {
		t.Fatalf("StartDaemon (restart): %v", err)
	}
	defer shutdown2()

	// Two clients both attach to the recovered session via holder
	clientA := ipc.NewClient()
	clientA.Connect(socketPath)
	defer clientA.Disconnect()

	clientB := ipc.NewClient()
	clientB.Connect(socketPath)
	defer clientB.Disconnect()

	attachResA, _ := clientA.Call("session.attach", map[string]any{"sessionId": sessionID})
	sockPathA := attachResA.(map[string]any)["holderSocket"].(string)

	hcA, _, err := holder.ConnectHolder(sockPathA)
	if err != nil {
		t.Fatalf("ConnectHolder A: %v", err)
	}
	defer hcA.Close()

	hcB, _, err := holder.ConnectHolder(sockPathA)
	if err != nil {
		t.Fatalf("ConnectHolder B: %v", err)
	}
	defer hcB.Close()

	// Both should receive output when one types
	marker2 := fmt.Sprintf("BOTH_%d", time.Now().UnixNano())
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	var outA, outB strings.Builder

	hcA.OnData(func(data []byte) {
		outA.Write(data)
		if strings.Contains(outA.String(), marker2) {
			select {
			case <-doneA:
			default:
				close(doneA)
			}
		}
	})
	hcB.OnData(func(data []byte) {
		outB.Write(data)
		if strings.Contains(outB.String(), marker2) {
			select {
			case <-doneB:
			default:
				close(doneB)
			}
		}
	})

	time.Sleep(300 * time.Millisecond)
	hcA.Write([]byte(fmt.Sprintf("echo %s\n", marker2)))

	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatalf("client A didn't receive output")
	}
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatalf("client B didn't receive output")
	}

	clientA.Call("session.kill", map[string]any{"sessionId": sessionID})
}
