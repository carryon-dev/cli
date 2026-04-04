//go:build !windows

package integration

import (
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

func TestSessionExitShell(t *testing.T) {
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

	// Create a real shell session (like user would)
	sess, err := client.Call("session.create", map[string]any{
		"name": "shell-exit-test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)
	t.Logf("Created shell session %s", sessionID)

	// Attach to get holder socket
	attachResult, err := client.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	attachMap := attachResult.(map[string]any)
	sockPath := attachMap["holderSocket"].(string)

	// Connect to holder directly
	hc, _, err := holder.ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder: %v", err)
	}

	// Register notification listener BEFORE sending exit
	gotNotification := make(chan string, 1)
	client.OnNotification("session.ended", func(params map[string]any) {
		t.Logf("Got session.ended: %v", params)
		if id, ok := params["sessionId"].(string); ok {
			gotNotification <- id
		}
	})

	// Give shell time to start
	time.Sleep(500 * time.Millisecond)

	// Type exit via holder
	t.Log("Sending 'exit\\n' to shell...")
	hc.Write([]byte("exit\n"))

	select {
	case id := <-gotNotification:
		t.Logf("SUCCESS: shell exit triggered session.ended for %s", id)
		hc.Close()
	case <-time.After(5 * time.Second):
		hc.Close()
		t.Fatal("TIMEOUT: shell did not exit after typing 'exit'")
	}
}
