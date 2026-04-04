package holder

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		path := SocketPath(`C:\Users\test\.carryon`, "sess-123")
		expected := `\\.\pipe\carryon-holder-sess-123`
		if path != expected {
			t.Fatalf("expected %q, got %q", expected, path)
		}
	} else {
		path := SocketPath("/tmp/.carryon", "sess-123")
		expected := filepath.Join("/tmp/.carryon", "holders", "sess-123.sock")
		if path != expected {
			t.Fatalf("expected %q, got %q", expected, path)
		}
	}
}

func TestListenAndDial(t *testing.T) {
	sockPath := testSocketPath(t)

	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	defer Cleanup(sockPath)

	// Accept in background
	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		// Echo everything back
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				conn.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	<-accepted

	// Test bidirectional I/O
	msg := "hello from client"
	_, err = conn.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	got := string(buf[:n])
	if got != msg {
		t.Fatalf("expected %q, got %q", msg, got)
	}
}

func TestListenDialBidirectional(t *testing.T) {
	sockPath := testSocketPath(t)

	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	defer Cleanup(sockPath)

	serverDone := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- ""
			return
		}
		defer conn.Close()

		// Server sends a message first
		_, _ = conn.Write([]byte("server-hello"))

		// Then reads from client
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		serverDone <- string(buf[:n])
	}()

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// Client reads server's message
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read from server failed: %v", err)
	}
	if string(buf[:n]) != "server-hello" {
		t.Fatalf("expected %q, got %q", "server-hello", string(buf[:n]))
	}

	// Client sends a message
	_, err = conn.Write([]byte("client-hello"))
	if err != nil {
		t.Fatalf("Write to server failed: %v", err)
	}

	serverGot := <-serverDone
	if serverGot != "client-hello" {
		t.Fatalf("server expected %q, got %q", "client-hello", serverGot)
	}
}

func TestListenStaleSocket(t *testing.T) {
	sockPath := testSocketPath(t)

	// Create a first listener to establish the socket file
	ln1, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("first Listen failed: %v", err)
	}
	ln1.Close()

	// Listen again on the same path -- should succeed by removing the stale socket
	ln2, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("second Listen failed (stale socket not cleaned): %v", err)
	}
	ln2.Close()
	Cleanup(sockPath)
}

func TestListenDialLargePayload(t *testing.T) {
	sockPath := testSocketPath(t)

	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()
	defer Cleanup(sockPath)

	// Build a large payload (64KB)
	payload := strings.Repeat("X", 64*1024)

	serverDone := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- ""
			return
		}
		defer conn.Close()

		var all []byte
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				all = append(all, buf[:n]...)
			}
			if len(all) >= len(payload) {
				break
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
		}
		serverDone <- string(all)
	}()

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	_, err = conn.Write([]byte(payload))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	conn.Close()

	serverGot := <-serverDone
	if serverGot != payload {
		t.Fatalf("payload mismatch: expected %d bytes, got %d bytes", len(payload), len(serverGot))
	}
}

// testSocketPath returns a unique, short socket path suitable for Unix domain sockets.
// Unix domain socket paths are limited to ~104 bytes on macOS, so we use /tmp directly
// with a short random name rather than t.TempDir() (which produces very long paths).
func testSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return SocketPath("", t.Name())
	}
	// Use a short temp directory to stay within Unix socket path length limits.
	dir, err := os.MkdirTemp("/tmp", "co")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}
