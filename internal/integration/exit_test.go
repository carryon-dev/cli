//go:build !windows

package integration

import (
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
)

func TestSessionExitNotification(t *testing.T) {
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

	// Register notification listener FIRST - before creating session
	gotNotification := make(chan string, 1)
	client.OnNotification("session.ended", func(params map[string]any) {
		if id, ok := params["sessionId"].(string); ok {
			gotNotification <- id
		}
	})

	// Create a long-lived session (shell), attach, then send exit
	sess, err := client.Call("session.create", map[string]any{
		"name": "exit-test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sm := sess.(map[string]any)
	sessionID := sm["id"].(string)

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
	defer hc.Close()

	// Give shell time to start, then send exit via holder
	time.Sleep(500 * time.Millisecond)
	hc.Write([]byte("exit\n"))

	select {
	case id := <-gotNotification:
		if id != sessionID {
			t.Fatalf("wrong session ID: %s vs %s", id, sessionID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT: never received session.ended notification")
	}
}
