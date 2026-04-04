//go:build !windows

// Tests for VS Code extension support features:
// - client.identify with type/name/pid
// - session.list returns clients array with PID for terminal matching
// - session.created/renamed broadcasts for sidebar updates
// - scrollback on attach for blank terminal fix
// - client disconnect removes from clients array

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

// TestExtensionStartupFlow simulates the VS Code extension startup:
// 1. Connect and identify as vscode
// 2. List sessions with client PIDs
// 3. Attach to a session and get scrollback
func TestExtensionStartupFlow(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Simulate a CLI client that already has a session
	cli := ipc.NewClient()
	if err := cli.Connect(socketPath); err != nil {
		t.Fatalf("Connect CLI: %v", err)
	}
	defer cli.Disconnect()

	cli.Call("client.identify", map[string]any{
		"type": "cli",
		"name": "Terminal",
		"pid":  float64(11111),
	})

	sess, _ := cli.Call("session.create", map[string]any{"name": "dev-server"})
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// CLI attaches, writes some output via holder
	attachResult, _ := cli.Call("session.attach", map[string]any{"sessionId": sessionID})
	attachMap := attachResult.(map[string]any)
	sockPath := attachMap["holderSocket"].(string)

	cliHc, _, _ := holder.ConnectHolder(sockPath)
	time.Sleep(300 * time.Millisecond)
	marker := fmt.Sprintf("STARTUP_%d", time.Now().UnixNano())
	cliHc.Write([]byte(fmt.Sprintf("echo %s\n", marker)))
	time.Sleep(500 * time.Millisecond)

	// --- Extension connects ---
	ext := ipc.NewClient()
	if err := ext.Connect(socketPath); err != nil {
		t.Fatalf("Connect extension: %v", err)
	}
	defer ext.Disconnect()

	// Step 1: Identify
	_, err = ext.Call("client.identify", map[string]any{
		"type": "vscode",
		"name": "VS Code",
		"pid":  float64(22222),
	})
	if err != nil {
		t.Fatalf("identify: %v", err)
	}

	// Step 2: List sessions - should see session with CLI client attached
	sessions, err := ext.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}

	sessInfo := list[0].(map[string]any)
	if sessInfo["id"] != sessionID {
		t.Fatalf("wrong session ID")
	}
	if sessInfo["name"] != "dev-server" {
		t.Fatalf("wrong session name: %v", sessInfo["name"])
	}

	clients := sessInfo["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 attached client, got %d", len(clients))
	}
	clientInfo := clients[0].(map[string]any)
	if clientInfo["type"] != "cli" {
		t.Fatalf("expected client type 'cli', got %v", clientInfo["type"])
	}
	if int(clientInfo["pid"].(float64)) != 11111 {
		t.Fatalf("expected client PID 11111, got %v", clientInfo["pid"])
	}

	// Step 3: Extension attaches - should get scrollback from holder with marker
	extAttachResult, _ := ext.Call("session.attach", map[string]any{"sessionId": sessionID})
	extAttachMap := extAttachResult.(map[string]any)
	extSockPath := extAttachMap["holderSocket"].(string)

	extHc, scrollback, err := holder.ConnectHolder(extSockPath)
	if err != nil {
		t.Fatalf("extension ConnectHolder: %v", err)
	}
	defer extHc.Close()

	// Check if scrollback already contains marker
	if !strings.Contains(string(scrollback), marker) {
		// Wait for it in the live stream
		done := make(chan struct{})
		var output strings.Builder
		output.Write(scrollback)
		extHc.OnData(func(data []byte) {
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
			// Scrollback received
		case <-time.After(5 * time.Second):
			t.Fatalf("scrollback not received. Got: %q", output.String())
		}
	}

	// Now session.list should show 2 clients
	time.Sleep(200 * time.Millisecond)
	sessions2, _ := ext.Call("session.list", nil)
	list2 := sessions2.([]any)
	sessInfo2 := list2[0].(map[string]any)
	clients2 := sessInfo2["clients"].([]any)
	if len(clients2) != 2 {
		t.Fatalf("expected 2 clients after extension attach, got %d: %v", len(clients2), clients2)
	}

	cliHc.Close()
	cli.Call("session.kill", map[string]any{"sessionId": sessionID})
}

// TestExtensionSidebarUpdates verifies that the extension receives
// session lifecycle broadcasts for live sidebar updates.
func TestExtensionSidebarUpdates(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Extension client watches for events
	ext := ipc.NewClient()
	if err := ext.Connect(socketPath); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer ext.Disconnect()
	ext.Call("client.identify", map[string]any{"type": "vscode", "name": "VS Code"})

	created := make(chan map[string]any, 5)
	renamed := make(chan map[string]any, 5)
	ended := make(chan map[string]any, 5)

	ext.OnNotification("session.created", func(p map[string]any) { created <- p })
	ext.OnNotification("session.renamed", func(p map[string]any) { renamed <- p })
	ext.OnNotification("session.ended", func(p map[string]any) { ended <- p })

	// Another client creates, renames, kills
	cli := ipc.NewClient()
	cli.Connect(socketPath)
	defer cli.Disconnect()

	sess, _ := cli.Call("session.create", map[string]any{"name": "sidebar-test"})
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	select {
	case p := <-created:
		if p["sessionId"] != sessionID {
			t.Fatalf("created: wrong sessionId")
		}
		if p["name"] != "sidebar-test" {
			t.Fatalf("created: wrong name: %v", p["name"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for session.created")
	}

	cli.Call("session.rename", map[string]any{"sessionId": sessionID, "name": "renamed-test"})

	select {
	case p := <-renamed:
		if p["sessionId"] != sessionID {
			t.Fatalf("renamed: wrong sessionId")
		}
		if p["name"] != "renamed-test" {
			t.Fatalf("renamed: wrong name: %v", p["name"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for session.renamed")
	}

	cli.Call("session.kill", map[string]any{"sessionId": sessionID})

	select {
	case p := <-ended:
		if p["sessionId"] != sessionID {
			t.Fatalf("ended: wrong sessionId")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.ended")
	}
}

// TestClientDisconnectRemovesFromClients verifies that when a client
// disconnects, it no longer appears in the session's clients array.
func TestClientDisconnectRemovesFromClients(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Persistent client
	persistent := ipc.NewClient()
	persistent.Connect(socketPath)
	defer persistent.Disconnect()
	persistent.Call("client.identify", map[string]any{"type": "cli", "name": "Persistent", "pid": float64(1)})

	sess, _ := persistent.Call("session.create", map[string]any{"name": "disconnect-test"})
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// Transient client attaches then disconnects
	transient := ipc.NewClient()
	transient.Connect(socketPath)
	transient.Call("client.identify", map[string]any{"type": "vscode", "name": "VS Code", "pid": float64(2)})

	transient.Call("session.attach", map[string]any{"sessionId": sessionID})
	time.Sleep(200 * time.Millisecond)

	// Should have 1 client (transient, persistent not attached)
	sessions, _ := persistent.Call("session.list", nil)
	list := sessions.([]any)
	sessInfo := list[0].(map[string]any)
	clients := sessInfo["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 client before disconnect, got %d", len(clients))
	}

	// Disconnect transient client
	transient.Disconnect()
	time.Sleep(300 * time.Millisecond)

	// Should have 0 clients
	sessions2, _ := persistent.Call("session.list", nil)
	list2 := sessions2.([]any)
	sessInfo2 := list2[0].(map[string]any)
	clients2 := sessInfo2["clients"].([]any)
	if len(clients2) != 0 {
		t.Fatalf("expected 0 clients after disconnect, got %d: %v", len(clients2), clients2)
	}

	persistent.Call("session.kill", map[string]any{"sessionId": sessionID})
}
