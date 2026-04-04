package holder

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testShell returns the shell to use for testing.
func testShell() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// testBaseDir returns a short temporary directory suitable for socket paths.
func testBaseDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "co")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestHolderSpawnAndHandshake(t *testing.T) {
	baseDir := testBaseDir(t)
	cwd, _ := os.Getwd()

	h, err := Spawn(SpawnOpts{
		SessionID: "t1",
		Shell:     testShell(),
		Cols:      100,
		Rows:      36,
		BaseDir:   baseDir,
		Cwd:       cwd,
		Command:   "test-shell",
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	if h.Pid() <= 0 {
		t.Fatalf("expected Pid > 0, got %d", h.Pid())
	}

	// Connect to the holder socket.
	sockPath := SocketPath(baseDir, "t1")
	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Read handshake.
	hs, _, err := readHandshakeFromConn(conn)
	if err != nil {
		t.Fatalf("reading handshake: %v", err)
	}

	if hs.PID == 0 {
		t.Fatal("expected PID != 0 in handshake")
	}
	if hs.HolderPID == 0 {
		t.Fatal("expected HolderPID != 0 in handshake")
	}
	if hs.Cols != 100 {
		t.Fatalf("expected Cols=100, got %d", hs.Cols)
	}
	if hs.Rows != 36 {
		t.Fatalf("expected Rows=36, got %d", hs.Rows)
	}
	if hs.Cwd != cwd {
		t.Fatalf("expected Cwd=%q, got %q", cwd, hs.Cwd)
	}
}

func TestHolderIORelay(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "t2",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "t2")
	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Read and discard handshake + any scrollback.
	_, _, err = readHandshakeFromConn(conn)
	if err != nil {
		t.Fatalf("reading handshake: %v", err)
	}

	// Send a command to the shell via FrameData.
	cmd := "echo HOLDER_MARKER_12345\n"
	frame := EncodeFrame(FrameData, []byte(cmd))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("Write command failed: %v", err)
	}

	// Read output until we find the marker.
	deadline := time.After(5 * time.Second)
	var accumulated []byte
	readBuf := make([]byte, readBufSize)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for marker in output; accumulated %d bytes: %q",
				len(accumulated), string(accumulated))
		default:
		}
		n, rerr := conn.Read(readBuf)
		if n > 0 {
			accumulated = append(accumulated, readBuf[:n]...)
			if strings.Contains(string(accumulated), "HOLDER_MARKER_12345") {
				return // success
			}
		}
		if rerr != nil {
			t.Fatalf("connection read error: %v; accumulated: %q", rerr, string(accumulated))
		}
	}
}

func TestHolderScrollbackOnReconnect(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "t3",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "t3")

	// First connection: send a command with a marker.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}

	_, _, err = readHandshakeFromConn(conn1)
	if err != nil {
		t.Fatalf("reading handshake 1: %v", err)
	}

	cmd := "echo SCROLLBACK_MARKER_99\n"
	frame := EncodeFrame(FrameData, []byte(cmd))
	if _, err := conn1.Write(frame); err != nil {
		t.Fatalf("Write command failed: %v", err)
	}

	// Wait for the output to appear (read it to be sure it was processed).
	deadline := time.After(5 * time.Second)
	readBuf := make([]byte, readBufSize)
	var acc []byte
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for marker on first connection; got: %q", string(acc))
		default:
		}
		n, rerr := conn1.Read(readBuf)
		if n > 0 {
			acc = append(acc, readBuf[:n]...)
			if strings.Contains(string(acc), "SCROLLBACK_MARKER_99") {
				break
			}
		}
		if rerr != nil {
			t.Fatalf("read error on conn1: %v", rerr)
		}
	}

	// Disconnect first connection.
	conn1.Close()
	// Brief pause to let the holder process the disconnect.
	time.Sleep(100 * time.Millisecond)

	// Second connection: verify scrollback contains the marker.
	conn2, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial 2 failed: %v", err)
	}
	defer conn2.Close()

	hs2, scrollData, err := readHandshakeFromConn(conn2)
	if err != nil {
		t.Fatalf("reading handshake 2: %v", err)
	}

	if hs2.ScrollbackLen == 0 {
		t.Fatal("expected non-zero ScrollbackLen on reconnect")
	}

	if !strings.Contains(string(scrollData), "SCROLLBACK_MARKER_99") {
		t.Fatalf("scrollback on reconnect does not contain marker; got %d bytes: %q",
			len(scrollData), string(scrollData))
	}
}

func TestHolderShellExit(t *testing.T) {
	baseDir := testBaseDir(t)

	// Use cmd.exe on Windows instead of PowerShell for fast startup.
	// PowerShell can take 10+ seconds to start on CI runners.
	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		args = []string{"/C", "exit 0"}
	} else {
		shell = testShell()
		args = []string{"-c", "exit 0"}
	}

	h, err := Spawn(SpawnOpts{
		SessionID: "t4",
		Shell:     shell,
		Args:      args,
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	// Verify the Done channel closes when the shell exits.
	select {
	case <-h.Done():
		// success
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Done channel")
	}
}

func TestHolderFrameExit(t *testing.T) {
	baseDir := testBaseDir(t)

	// Use a short sleep then exit so the shell lives long enough for us to
	// connect and read the handshake before it exits.
	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		args = []string{"/C", "ping -n 2 127.0.0.1 >nul & exit 0"}
	} else {
		shell = testShell()
		args = []string{"-c", "sleep 1; exit 0"}
	}

	h, err := Spawn(SpawnOpts{
		SessionID: "t5",
		Shell:     shell,
		Args:      args,
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "t5")
	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Read handshake.
	_, _, herr := readHandshakeFromConn(conn)
	if herr != nil {
		t.Fatalf("handshake failed: %v", herr)
	}

	// Read frames until FrameExit or connection close.
	deadline := time.After(30 * time.Second)
	var buf []byte
	tmp := make([]byte, readBufSize)
	gotExit := false

	for !gotExit {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for FrameExit; accumulated %d bytes", len(buf))
		case <-h.Done():
			// Holder exited - on some platforms the FrameExit frame may not
			// be readable after the holder closes the connection.
			gotExit = true
			continue
		default:
		}

		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, rerr := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		for {
			typ, _, frest, ferr := DecodeFrame(buf)
			if ferr != nil {
				break
			}
			buf = frest
			if typ == FrameExit {
				gotExit = true
				break
			}
		}

		if rerr != nil {
			if os.IsTimeout(rerr) {
				continue
			}
			// Connection closed; shell exited and holder cleaned up.
			break
		}
	}
}

// readHandshakeFromConn reads a complete handshake from a connection.
// It also reads the scrollback bytes indicated by ScrollbackLen and returns them.
func readHandshakeFromConn(conn interface {
	Read([]byte) (int, error)
}) (Handshake, []byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		hs, rest, herr := DecodeHandshake(buf)
		if herr == nil {
			// We have the handshake. Now consume ScrollbackLen bytes.
			need := int(hs.ScrollbackLen)
			for len(rest) < need {
				n2, err2 := conn.Read(tmp)
				if n2 > 0 {
					rest = append(rest, tmp[:n2]...)
				}
				if err2 != nil {
					return hs, rest, err2
				}
			}
			scrollback := make([]byte, need)
			copy(scrollback, rest[:need])
			return hs, scrollback, nil
		}

		if err != nil {
			return Handshake{}, nil, err
		}
	}
}

// readHandshakeFromNetConn reads a complete handshake from a net.Conn,
// returning the handshake, scrollback data, and any trailing bytes after scrollback.
func readHandshakeFromNetConn(conn net.Conn) (Handshake, []byte, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		hs, rest, herr := DecodeHandshake(buf)
		if herr == nil {
			needed := int(hs.ScrollbackLen)
			if len(rest) >= needed {
				return hs, rest[:needed], nil
			}
			buf = rest
			for len(buf) < needed {
				n, err := conn.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[:n]...)
				}
				if err != nil {
					return Handshake{}, nil, fmt.Errorf("read scrollback: %w", err)
				}
			}
			return hs, buf[:needed], nil
		}

		if err != nil {
			return Handshake{}, nil, fmt.Errorf("read handshake: %w", err)
		}
	}
}

// waitForMarker reads frames from conn until it finds a FrameData frame
// containing the marker string, or the timeout expires.
func waitForMarker(t *testing.T, conn net.Conn, marker string, timeout time.Duration) bool {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		for {
			typ, payload, rest, ferr := DecodeFrame(buf)
			if ferr != nil {
				break
			}
			buf = rest
			if typ == FrameData && strings.Contains(string(payload), marker) {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

func TestHolderMultiClient(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "mc1",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "mc1")

	// Connect client 1.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 1 failed: %v", err)
	}
	defer conn1.Close()

	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake client 1: %v", err)
	}

	// Connect client 2.
	conn2, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 2 failed: %v", err)
	}
	defer conn2.Close()

	_, _, err = readHandshakeFromNetConn(conn2)
	if err != nil {
		t.Fatalf("handshake client 2: %v", err)
	}

	// Send a command from client 1.
	marker := "MULTI_CLIENT_MARKER_42"
	cmd := "echo " + marker + "\n"
	frame := EncodeFrame(FrameData, []byte(cmd))
	if _, err := conn1.Write(frame); err != nil {
		t.Fatalf("Write command failed: %v", err)
	}

	// Both clients should see the marker in the PTY output.
	got1 := waitForMarker(t, conn1, marker, 5*time.Second)
	got2 := waitForMarker(t, conn2, marker, 5*time.Second)

	if !got1 {
		t.Fatal("client 1 did not receive marker output")
	}
	if !got2 {
		t.Fatal("client 2 did not receive marker output")
	}
}

func TestHolderResizeMultiClient(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "resize-mc",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "resize-mc")

	// Connect client 1.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 1 failed: %v", err)
	}
	defer conn1.Close()

	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake client 1: %v", err)
	}

	// Connect client 2.
	conn2, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 2 failed: %v", err)
	}
	defer conn2.Close()

	_, _, err = readHandshakeFromNetConn(conn2)
	if err != nil {
		t.Fatalf("handshake client 2: %v", err)
	}

	// Client 1 sends resize to 120x40.
	if _, err := conn1.Write(EncodeResize(120, 40)); err != nil {
		t.Fatalf("client 1 resize write failed: %v", err)
	}

	// Wait for holder to reflect client 1's resize before sending client 2's,
	// so that client 2's resize is guaranteed to be processed last.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := Dial(sockPath)
		if err != nil {
			t.Fatalf("Dial probe (resize 1): %v", err)
		}
		hs, _, err := readHandshakeFromNetConn(probe)
		probe.Close()
		if err != nil {
			t.Fatalf("Handshake probe (resize 1): %v", err)
		}
		if hs.Cols == 120 && hs.Rows == 40 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Client 2 sends resize to 100x30 (last write wins).
	if _, err := conn2.Write(EncodeResize(100, 30)); err != nil {
		t.Fatalf("client 2 resize write failed: %v", err)
	}

	// Wait for holder to reflect client 2's resize.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := Dial(sockPath)
		if err != nil {
			t.Fatalf("Dial probe (resize 2): %v", err)
		}
		hs, _, err := readHandshakeFromNetConn(probe)
		probe.Close()
		if err != nil {
			t.Fatalf("Handshake probe (resize 2): %v", err)
		}
		if hs.Cols == 100 && hs.Rows == 30 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("holder did not reflect client 2's resize (100x30) within timeout")
}

func TestHolderStatusQuery(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "status1",
		Shell:     testShell(),
		Cols:      100,
		Rows:      50,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "status1")
	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Read handshake (and scrollback).
	_, _, err = readHandshakeFromNetConn(conn)
	if err != nil {
		t.Fatalf("reading handshake: %v", err)
	}

	// Send a FrameStatusRequest.
	reqFrame := EncodeFrame(FrameStatusRequest, nil)
	if _, err := conn.Write(reqFrame); err != nil {
		t.Fatalf("Write FrameStatusRequest failed: %v", err)
	}

	// Read response frames, skipping any FrameData from PTY output,
	// until we receive a FrameStatusResponse.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var buf []byte
	tmp := make([]byte, readBufSize)
	for {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		for {
			typ, payload, rest, ferr := DecodeFrame(buf)
			if ferr != nil {
				break // need more data
			}
			buf = rest

			if typ == FrameData {
				continue // skip PTY output
			}
			if typ != FrameStatusResponse {
				t.Fatalf("unexpected frame type 0x%02x", typ)
			}

			sr, derr := DecodeStatusResponse(payload)
			if derr != nil {
				t.Fatalf("DecodeStatusResponse: %v", derr)
			}
			if sr.Cols != 100 {
				t.Fatalf("expected Cols=100, got %d", sr.Cols)
			}
			if sr.Rows != 50 {
				t.Fatalf("expected Rows=50, got %d", sr.Rows)
			}
			if sr.ClientCount != 1 {
				t.Fatalf("expected ClientCount=1, got %d", sr.ClientCount)
			}
			return // success
		}

		if rerr != nil {
			t.Fatalf("connection read error waiting for FrameStatusResponse: %v", rerr)
		}
	}
}

func TestHolderResizeRequestOnInputFromDifferentClient(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "resizereq1",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "resizereq1")

	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 1 failed: %v", err)
	}
	defer conn1.Close()
	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake client 1: %v", err)
	}

	conn2, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 2 failed: %v", err)
	}
	defer conn2.Close()
	_, _, err = readHandshakeFromNetConn(conn2)
	if err != nil {
		t.Fatalf("handshake client 2: %v", err)
	}

	// Client 1 sends resize (becomes lastResizeConn).
	if _, err := conn1.Write(EncodeResize(120, 40)); err != nil {
		t.Fatalf("client 1 resize write failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Client 2 sends input - should trigger FrameResizeRequest back to client 2.
	if _, err := conn2.Write(EncodeFrame(FrameData, []byte("x"))); err != nil {
		t.Fatalf("client 2 input write failed: %v", err)
	}

	conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	var found bool
	for !found {
		n, rerr := conn2.Read(buf)
		if n > 0 {
			data := buf[:n]
			for len(data) > 0 {
				typ, _, rest, ferr := DecodeFrame(data)
				if ferr != nil {
					break
				}
				data = rest
				if typ == FrameResizeRequest {
					found = true
					break
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if !found {
		t.Fatal("expected FrameResizeRequest on client 2 after input, but did not receive one")
	}
}

func TestHolderNoResizeRequestWhenSameClient(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "resizereq2",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "resizereq2")

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()
	_, _, err = readHandshakeFromNetConn(conn)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if _, err := conn.Write(EncodeResize(120, 40)); err != nil {
		t.Fatalf("resize write failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if _, err := conn.Write(EncodeFrame(FrameData, []byte("x"))); err != nil {
		t.Fatalf("input write failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 4096)
	for {
		n, rerr := conn.Read(buf)
		if n > 0 {
			data := buf[:n]
			for len(data) > 0 {
				typ, _, rest, ferr := DecodeFrame(data)
				if ferr != nil {
					break
				}
				data = rest
				if typ == FrameResizeRequest {
					t.Fatal("received unexpected FrameResizeRequest from same client")
				}
			}
		}
		if rerr != nil {
			break
		}
	}
}

func TestHolderClientDisconnectIsolation(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "mc2",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "mc2")

	// Connect client 1.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 1 failed: %v", err)
	}

	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake client 1: %v", err)
	}

	// Connect client 2.
	conn2, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial client 2 failed: %v", err)
	}
	defer conn2.Close()

	_, _, err = readHandshakeFromNetConn(conn2)
	if err != nil {
		t.Fatalf("handshake client 2: %v", err)
	}

	// Disconnect client 1.
	conn1.Close()
	time.Sleep(100 * time.Millisecond)

	// Client 2 should still work - send a command and verify output.
	marker := "ISOLATION_MARKER_77"
	cmd := "echo " + marker + "\n"
	frame := EncodeFrame(FrameData, []byte(cmd))
	if _, err := conn2.Write(frame); err != nil {
		t.Fatalf("Write command to client 2 failed: %v", err)
	}

	got := waitForMarker(t, conn2, marker, 5*time.Second)
	if !got {
		t.Fatal("client 2 did not receive marker output after client 1 disconnected")
	}
}
