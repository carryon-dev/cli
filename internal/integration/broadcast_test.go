//go:build !windows

package integration

import (
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/ipc"
)

func TestSessionCreatedBroadcast(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Client 1: the observer (not attached to anything)
	observer := ipc.NewClient()
	if err := observer.Connect(socketPath); err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	defer observer.Disconnect()

	gotCreated := make(chan map[string]any, 1)
	observer.OnNotification("session.created", func(params map[string]any) {
		gotCreated <- params
	})

	// Client 2: creates a session
	creator := ipc.NewClient()
	if err := creator.Connect(socketPath); err != nil {
		t.Fatalf("connect creator: %v", err)
	}
	defer creator.Disconnect()

	sess, err := creator.Call("session.create", map[string]any{
		"name": "broadcast-create-test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// Observer should receive session.created
	select {
	case params := <-gotCreated:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s, got %v", sessionID, params["sessionId"])
		}
		if params["name"] != "broadcast-create-test" {
			t.Fatalf("expected name 'broadcast-create-test', got %v", params["name"])
		}
		if params["backend"] != "native" {
			t.Fatalf("expected backend 'native', got %v", params["backend"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.created broadcast")
	}

	// Clean up
	creator.Call("session.kill", map[string]any{"sessionId": sessionID})
}

func TestSessionRenamedBroadcast(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	observer := ipc.NewClient()
	if err := observer.Connect(socketPath); err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	defer observer.Disconnect()

	gotRenamed := make(chan map[string]any, 1)
	observer.OnNotification("session.renamed", func(params map[string]any) {
		gotRenamed <- params
	})

	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer client.Disconnect()

	sess, err := client.Call("session.create", map[string]any{"name": "before-rename"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	_, err = client.Call("session.rename", map[string]any{
		"sessionId": sessionID,
		"name":      "after-rename",
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}

	select {
	case params := <-gotRenamed:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s, got %v", sessionID, params["sessionId"])
		}
		if params["name"] != "after-rename" {
			t.Fatalf("expected name 'after-rename', got %v", params["name"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.renamed broadcast")
	}

	client.Call("session.kill", map[string]any{"sessionId": sessionID})
}

func TestSessionEndedBroadcastToUnattachedClient(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Observer is NOT attached to the session
	observer := ipc.NewClient()
	if err := observer.Connect(socketPath); err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	defer observer.Disconnect()

	gotEnded := make(chan map[string]any, 1)
	observer.OnNotification("session.ended", func(params map[string]any) {
		gotEnded <- params
	})

	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer client.Disconnect()

	sess, err := client.Call("session.create", map[string]any{"name": "ended-test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	// Kill the session - observer should get session.ended even though not attached
	_, err = client.Call("session.kill", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case params := <-gotEnded:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s, got %v", sessionID, params["sessionId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.ended broadcast to unattached client")
	}
}

func TestSessionAttachDetachBroadcast(t *testing.T) {
	baseDir := shortTempDir(t)
	shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir, InProcess: true})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer shutdown()

	socketPath := daemon.GetSocketPath(baseDir)

	// Observer watches for attach/detach
	observer := ipc.NewClient()
	if err := observer.Connect(socketPath); err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	defer observer.Disconnect()

	gotAttached := make(chan map[string]any, 1)
	gotDetached := make(chan map[string]any, 1)

	observer.OnNotification("session.attached", func(params map[string]any) {
		gotAttached <- params
	})
	observer.OnNotification("session.detached", func(params map[string]any) {
		gotDetached <- params
	})

	// Client creates and attaches
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		t.Fatalf("connect client: %v", err)
	}

	sess, err := client.Call("session.create", map[string]any{"name": "attach-test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

	_, err = client.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Wait for attach broadcast
	select {
	case params := <-gotAttached:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s in attached event, got %v", sessionID, params["sessionId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.attached broadcast")
	}

	// Disconnect client - should trigger detach
	client.Disconnect()

	// Wait for detach broadcast
	select {
	case params := <-gotDetached:
		if params["sessionId"] != sessionID {
			t.Fatalf("expected sessionId %s in detached event, got %v", sessionID, params["sessionId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session.detached broadcast")
	}

	// Clean up
	client2 := ipc.NewClient()
	if err := client2.Connect(socketPath); err == nil {
		client2.Call("session.kill", map[string]any{"sessionId": sessionID})
		client2.Disconnect()
	}
}
