package daemon

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/state"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "cod-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestRecoveryCleansStaleNoSocket(t *testing.T) {
	tmpDir := t.TempDir()

	sessionState := state.NewSessionState(tmpDir)
	logStore := logging.NewStore("", 0)
	logger := logging.NewLogger(logStore, "debug")

	// Save a session - no holder socket exists for it.
	sessionState.Save(backend.Session{
		ID:      "native-deadbeef",
		Name:    "stale-session",
		Backend: "native",
		PID:     999999,
		Created: time.Now().Add(-10 * time.Second).UnixMilli(),
	})

	// Verify it was saved
	all := sessionState.GetAll()
	if len(all) != 1 {
		t.Fatalf("Expected 1 saved session, got %d", len(all))
	}

	result := RecoverSessions(sessionState, logger, tmpDir)

	if result.Cleaned != 1 {
		t.Errorf("RecoverSessions().Cleaned = %d, want 1", result.Cleaned)
	}
	if result.Recovered != 0 {
		t.Errorf("RecoverSessions().Recovered = %d, want 0", result.Recovered)
	}

	// Session should be removed from state
	remaining := sessionState.GetAll()
	if len(remaining) != 0 {
		t.Errorf("Expected 0 sessions after recovery, got %d", len(remaining))
	}
}

func TestRecoverySkipsNonNative(t *testing.T) {
	tmpDir := t.TempDir()

	sessionState := state.NewSessionState(tmpDir)
	logStore := logging.NewStore("", 0)
	logger := logging.NewLogger(logStore, "debug")

	// Save a tmux session -- recovery should skip it
	sessionState.Save(backend.Session{
		ID:      "tmux-abc123",
		Name:    "tmux-session",
		Backend: "tmux",
		Created: time.Now().UnixMilli(),
	})

	result := RecoverSessions(sessionState, logger, tmpDir)

	if result.Cleaned != 0 {
		t.Errorf("RecoverSessions().Cleaned = %d, want 0", result.Cleaned)
	}
	if result.Recovered != 0 {
		t.Errorf("RecoverSessions().Recovered = %d, want 0", result.Recovered)
	}

	// Tmux session should remain
	remaining := sessionState.GetAll()
	if len(remaining) != 1 {
		t.Errorf("Expected 1 session remaining, got %d", len(remaining))
	}
}

func testShell() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func TestRecoveryDetectsLiveHolder(t *testing.T) {
	tmpDir := shortTempDir(t)

	sessionState := state.NewSessionState(tmpDir)
	logStore := logging.NewStore("", 0)
	logger := logging.NewLogger(logStore, "debug")

	sessionID := "native-abcdef123456"

	// Create a fake holder socket that accepts connections.
	// Use holder.Listen/SocketPath for cross-platform support (Unix socket vs named pipe).
	sockPath := holder.SocketPath(tmpDir, sessionID)
	ln, err := holder.Listen(sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept connections in the background (the recovery probe will connect).
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	sessionState.Save(backend.Session{
		ID:      sessionID,
		Name:    "live-session",
		Backend: "native",
		PID:     12345,
		Created: time.Now().UnixMilli(),
	})

	result := RecoverSessions(sessionState, logger, tmpDir)

	if result.Recovered != 1 {
		t.Errorf("RecoverSessions().Recovered = %d, want 1", result.Recovered)
	}
	if result.Cleaned != 0 {
		t.Errorf("RecoverSessions().Cleaned = %d, want 0", result.Cleaned)
	}

	// Session should still be in state
	remaining := sessionState.GetAll()
	if len(remaining) != 1 {
		t.Errorf("Expected 1 session remaining, got %d", len(remaining))
	}
}

func TestQueryHolderStatus(t *testing.T) {
	baseDir := shortTempDir(t)

	h, err := holder.Spawn(holder.SpawnOpts{
		SessionID: "qhs-test",
		Shell:     testShell(),
		Cols:      120,
		Rows:      40,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	// Give the holder a moment to start accepting connections.
	time.Sleep(200 * time.Millisecond)

	sockPath := holder.SocketPath(baseDir, "qhs-test")
	conn, err := holder.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	status, err := queryHolderStatus(conn)
	if err != nil {
		t.Fatalf("queryHolderStatus failed: %v", err)
	}

	if status.Cols != 120 {
		t.Errorf("expected Cols=120, got %d", status.Cols)
	}
	if status.Rows != 40 {
		t.Errorf("expected Rows=40, got %d", status.Rows)
	}
	if status.PID == 0 {
		t.Error("expected PID > 0")
	}
	if status.HolderPID == 0 {
		t.Error("expected HolderPID > 0")
	}
	// The query connection itself is registered as a client after the
	// handshake is sent, so the status response should report 1 client.
	if status.ClientCount != 1 {
		t.Errorf("expected ClientCount=1, got %d", status.ClientCount)
	}
}

func TestQueryHolderStatusWithFrameData(t *testing.T) {
	baseDir := shortTempDir(t)

	h, err := holder.Spawn(holder.SpawnOpts{
		SessionID: "qhs-data",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := holder.SocketPath(baseDir, "qhs-data")

	// Connect a helper client first to generate PTY output.
	helperConn, err := holder.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial helper failed: %v", err)
	}
	defer helperConn.Close()

	// Read handshake from helper connection so the holder registers it.
	helperConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hsBuf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, rerr := helperConn.Read(tmp)
		if n > 0 {
			hsBuf = append(hsBuf, tmp[:n]...)
		}
		hs, rest, decErr := holder.DecodeHandshake(hsBuf)
		if decErr == nil {
			// Consume scrollback.
			need := int(hs.ScrollbackLen)
			for len(rest) < need {
				n2, err2 := helperConn.Read(tmp)
				if n2 > 0 {
					rest = append(rest, tmp[:n2]...)
				}
				if err2 != nil {
					t.Fatalf("read scrollback: %v", err2)
				}
			}
			break
		}
		if rerr != nil {
			t.Fatalf("read handshake: %v", rerr)
		}
	}
	helperConn.SetReadDeadline(time.Time{})

	// Send a command through the helper to generate PTY output that will be
	// broadcast to all clients (including the query connection we open next).
	marker := "QHSDATA_MARKER_12345"
	cmd := holder.EncodeFrame(holder.FrameData, []byte("echo "+marker+"\n"))
	if _, err := helperConn.Write(cmd); err != nil {
		t.Fatalf("Write command failed: %v", err)
	}

	// Wait for the output to appear in the helper stream so we know the
	// holder has generated FrameData that will be in-flight for the next client.
	helperConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var helperOut []byte
	for {
		n, rerr := helperConn.Read(tmp)
		if n > 0 {
			helperOut = append(helperOut, tmp[:n]...)
		}
		if strings.Contains(string(helperOut), marker) {
			break
		}
		if rerr != nil {
			t.Fatalf("helper read error: %v; output so far: %q", rerr, string(helperOut))
		}
	}
	helperConn.SetReadDeadline(time.Time{})

	// Now open the query connection. The holder will send handshake + scrollback
	// (which contains the echoed marker output). queryHolderStatus must skip
	// any FrameData frames mixed in and still find the StatusResponse.
	queryConn, err := holder.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial query failed: %v", err)
	}
	defer queryConn.Close()

	status, err := queryHolderStatus(queryConn)
	if err != nil {
		t.Fatalf("queryHolderStatus failed: %v", err)
	}

	if status.Cols != 80 {
		t.Errorf("expected Cols=80, got %d", status.Cols)
	}
	if status.Rows != 24 {
		t.Errorf("expected Rows=24, got %d", status.Rows)
	}
	if status.PID == 0 {
		t.Error("expected PID > 0")
	}
	if status.HolderPID == 0 {
		t.Error("expected HolderPID > 0")
	}
	// Two clients: the helper connection and the query connection.
	if status.ClientCount != 2 {
		t.Errorf("expected ClientCount=2, got %d", status.ClientCount)
	}
}
