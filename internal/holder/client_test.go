package holder

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHolderClientConnectAndIO(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "hc1",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "hc1")
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	// Collect output.
	dataCh := make(chan string, 64)
	hc.OnData(func(data []byte) {
		dataCh <- string(data)
	})

	// Write an echo command.
	marker := "HOLDERCLIENT_MARKER_789"
	if err := hc.Write([]byte("echo " + marker + "\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Wait for the marker in output.
	deadline := time.After(5 * time.Second)
	var accumulated string
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for marker; accumulated: %q", accumulated)
		case chunk := <-dataCh:
			accumulated += chunk
			if strings.Contains(accumulated, marker) {
				return // success
			}
		}
	}
}

func TestHolderClientResize(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "hc2",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "hc2")
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	if err := hc.Resize(120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestHolderClientConnectRefused(t *testing.T) {
	baseDir := testBaseDir(t)

	// Use a socket path where no holder is listening.
	sockPath := SocketPath(baseDir, "nonexistent")
	_, _, err := ConnectHolder(sockPath)
	if err == nil {
		t.Fatal("expected error connecting to nonexistent socket, got nil")
	}
}

func TestHolderClientExitCallback(t *testing.T) {
	baseDir := testBaseDir(t)

	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		args = []string{"/C", "ping -n 2 127.0.0.1 >nul & exit 42"}
	} else {
		shell = testShell()
		args = []string{"-c", "sleep 1; exit 42"}
	}

	h, err := Spawn(SpawnOpts{
		SessionID: "hc-exit",
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

	sockPath := SocketPath(baseDir, "hc-exit")
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	exitCh := make(chan int32, 1)
	hc.OnExit(func(code int32) {
		exitCh <- code
	})

	select {
	case code := <-exitCh:
		if code != 42 {
			t.Fatalf("expected exit code 42, got %d", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for OnExit callback")
	}
}

func TestHolderClientUnexpectedDisconnect(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "hc-disconnect",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	sockPath := SocketPath(baseDir, "hc-disconnect")
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	exitCh := make(chan int32, 1)
	hc.OnExit(func(code int32) {
		exitCh <- code
	})

	// Forcibly shut down the holder to simulate unexpected disconnect.
	h.Shutdown()

	select {
	case code := <-exitCh:
		if code != -1 {
			t.Fatalf("expected exit code -1 for unexpected disconnect, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for OnExit callback after unexpected disconnect")
	}
}

func TestHolderClientWriteAfterClose(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "hc-wac",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "hc-wac")
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}

	// Close the client, then attempt to write.
	hc.Close()

	err = hc.Write([]byte("hello\n"))
	if err == nil {
		t.Fatal("expected error writing after close, got nil")
	}
}

func TestHolderClientResizeRequest(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "clientresreq1",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "clientresreq1")

	// Connect client 1 (raw conn) to establish lastResizeConn.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial raw client failed: %v", err)
	}
	defer conn1.Close()
	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake raw client: %v", err)
	}

	// Client 1 sends resize to become lastResizeConn.
	if _, err := conn1.Write(EncodeResize(120, 40)); err != nil {
		t.Fatalf("raw client resize write failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Connect client 2 via HolderClient.
	hc, _, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	// Register resize request callback.
	called := make(chan struct{}, 1)
	hc.OnResizeRequest(func() {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// Client 2 (HolderClient) sends input - should trigger resize request callback.
	if err := hc.Write([]byte("x")); err != nil {
		t.Fatalf("HolderClient write failed: %v", err)
	}

	select {
	case <-called:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("OnResizeRequest callback not called within timeout")
	}
}

func TestHolderClientScrollbackDelivery(t *testing.T) {
	baseDir := testBaseDir(t)

	h, err := Spawn(SpawnOpts{
		SessionID: "hc-scroll",
		Shell:     testShell(),
		Cols:      80,
		Rows:      24,
		BaseDir:   baseDir,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer h.Shutdown()

	sockPath := SocketPath(baseDir, "hc-scroll")

	// First connection: use raw Dial to send a command, wait for output, then disconnect.
	conn1, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}

	_, _, err = readHandshakeFromNetConn(conn1)
	if err != nil {
		t.Fatalf("handshake 1: %v", err)
	}

	marker := "SCROLLBACK_CLIENT_MARKER_55"
	cmd := "echo " + marker + "\n"
	frame := EncodeFrame(FrameData, []byte(cmd))
	if _, err := conn1.Write(frame); err != nil {
		t.Fatalf("Write command failed: %v", err)
	}

	if !waitForMarker(t, conn1, marker, 5*time.Second) {
		t.Fatal("did not see marker on first connection")
	}

	conn1.Close()
	time.Sleep(100 * time.Millisecond)

	// Second connection: use ConnectHolder and check scrollback.
	hc, scrollback, err := ConnectHolder(sockPath)
	if err != nil {
		t.Fatalf("ConnectHolder failed: %v", err)
	}
	defer hc.Close()

	if !strings.Contains(string(scrollback), marker) {
		t.Fatalf("scrollback does not contain marker; got %d bytes: %q",
			len(scrollback), string(scrollback))
	}
}
