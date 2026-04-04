//go:build !windows

package integration

import (
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/ipc"
)

func TestClientIdentifyAndSessionClients(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Client 1: VS Code extension
	client1 := ipc.NewClient()
	if err := client1.Connect(socketPath); err != nil {
		t.Fatalf("Connect client1: %v", err)
	}
	defer client1.Disconnect()

	_, err = client1.Call("client.identify", map[string]any{
		"type": "vscode",
		"name": "VS Code",
		"pid":  float64(12345),
	})
	if err != nil {
		t.Fatalf("client.identify: %v", err)
	}

	// Client 2: CLI terminal
	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err != nil {
		t.Fatalf("Connect client2: %v", err)
	}
	defer client2.Disconnect()

	_, err = client2.Call("client.identify", map[string]any{
		"type": "cli",
		"name": "Terminal",
		"pid":  float64(54321),
	})
	if err != nil {
		t.Fatalf("client.identify: %v", err)
	}

	// Create a session and both attach
	sess, err := client1.Call("session.create", map[string]any{"name": "identify-test"})
	if err != nil {
		t.Fatalf("session.create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	_, err = client1.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("client1 attach: %v", err)
	}

	_, err = client2.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("client2 attach: %v", err)
	}

	// Wait for PostRPC to execute
	time.Sleep(200 * time.Millisecond)

	// List sessions - should include clients array with both clients
	sessions, err := client1.Call("session.list", nil)
	if err != nil {
		t.Fatalf("session.list: %v", err)
	}
	list := sessions.([]any)
	if len(list) == 0 {
		t.Fatal("expected at least one session")
	}

	var found map[string]any
	for _, s := range list {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatalf("session %s not found in list", sessionID)
	}

	clients, ok := found["clients"].([]any)
	if !ok {
		t.Fatalf("expected clients array, got %T: %v", found["clients"], found["clients"])
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d: %v", len(clients), clients)
	}

	// Check that client info is present
	types := map[string]bool{}
	for _, c := range clients {
		cm := c.(map[string]any)
		ct, _ := cm["type"].(string)
		types[ct] = true

		// Verify fields are present
		if cm["clientId"] == nil || cm["clientId"] == "" {
			t.Fatal("client missing clientId")
		}
		if cm["connectedAt"] == nil {
			t.Fatal("client missing connectedAt")
		}
	}
	if !types["vscode"] {
		t.Fatal("expected a vscode client")
	}
	if !types["cli"] {
		t.Fatal("expected a cli client")
	}

	// Clean up
	client1.Call("session.kill", map[string]any{"sessionId": sessionID})
}

func TestClientIdentifyPIDMatching(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
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

	_, err = client.Call("client.identify", map[string]any{
		"type": "vscode",
		"name": "VS Code",
		"pid":  float64(99999),
	})
	if err != nil {
		t.Fatalf("identify: %v", err)
	}

	sess, _ := client.Call("session.create", map[string]any{"name": "pid-test"})
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	client.Call("session.attach", map[string]any{"sessionId": sessionID})
	time.Sleep(200 * time.Millisecond)

	// List and verify PID is in the clients array
	sessions, _ := client.Call("session.list", nil)
	list := sessions.([]any)
	for _, s := range list {
		m := s.(map[string]any)
		if m["id"] == sessionID {
			clients := m["clients"].([]any)
			if len(clients) != 1 {
				t.Fatalf("expected 1 client, got %d", len(clients))
			}
			cm := clients[0].(map[string]any)
			pid := int(cm["pid"].(float64))
			if pid != 99999 {
				t.Fatalf("expected pid 99999, got %d", pid)
			}
			clientType := cm["type"].(string)
			if clientType != "vscode" {
				t.Fatalf("expected type vscode, got %s", clientType)
			}
			break
		}
	}

	client.Call("session.kill", map[string]any{"sessionId": sessionID})
}
