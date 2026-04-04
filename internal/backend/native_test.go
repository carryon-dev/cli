package backend_test

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
)

// waitForGoroutineCount polls until the number of goroutines is <= target
// or the deadline is exceeded. It returns true on success.
func waitForGoroutineCount(target int, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if runtime.NumGoroutine() <= target {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// shortTempDir creates a temp directory with a short path to stay within
// the Unix socket path length limit (104 bytes on macOS).
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "cob-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// testInfiniteEchoCommand returns a command that continuously produces output.
// On Unix, uses a while loop. On Windows, uses a PowerShell loop.
func testInfiniteEchoCommand() string {
	if runtime.GOOS == "windows" {
		return "while($true){echo ping;Start-Sleep -Milliseconds 100}"
	}
	return "while true; do echo ping; sleep 0.1; done"
}

func TestNativeCreateAndList(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Name:    "test-session",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if sess.Name != "test-session" {
		t.Fatalf("expected name 'test-session', got %q", sess.Name)
	}
	if sess.Backend != "native" {
		t.Fatalf("expected backend 'native', got %q", sess.Backend)
	}
	if !strings.HasPrefix(sess.ID, "native-") {
		t.Fatalf("expected ID to start with 'native-', got %q", sess.ID)
	}
	if len(sess.ID) != len("native-")+12 {
		t.Fatalf("expected ID length %d, got %d (%q)", len("native-")+12, len(sess.ID), sess.ID)
	}

	list := b.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session in list, got %d", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("listed session ID mismatch: %q != %q", list[0].ID, sess.ID)
	}
}

func TestNativeAttachAndRead(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Command: "echo hello-native; sleep 30",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	sh, err := b.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer sh.Close()

	received := make(chan string, 1)
	sh.OnData(func(data []byte) int {
		s := string(data)
		if strings.Contains(s, "hello-native") {
			select {
			case received <- s:
			default:
			}
		}
		return 0
	})

	select {
	case <-received:
		// success
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for output from attached stream")
	}
}

func TestNativeKill(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Command: "sleep 60",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := b.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Wait briefly for process exit cleanup
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session to be removed after kill")
		default:
		}
		list := b.List()
		if len(list) == 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNativeRename(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Name:    "original",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := b.Rename(sess.ID, "renamed"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	list := b.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].Name != "renamed" {
		t.Fatalf("expected name 'renamed', got %q", list[0].Name)
	}
}

func TestNativeScrollback(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	// Use a long sleep to keep the session alive while we poll scrollback.
	// The echo output must be read before the session ends, otherwise
	// handleSessionEnded removes the process and GetScrollback returns "".
	sess, err := b.Create(backend.CreateOpts{
		Command: "echo scrollback-test-data; sleep 30",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			sb := b.GetScrollback(sess.ID)
			t.Fatalf("timed out waiting for scrollback data; current scrollback (%d bytes): %q", len(sb), sb)
		default:
		}
		sb := b.GetScrollback(sess.ID)
		if strings.Contains(sb, "scrollback-test-data") {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNativeOnSessionCreated(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	received := make(chan backend.Session, 1)
	b.OnSessionCreated(func(sess backend.Session) {
		select {
		case received <- sess:
		default:
		}
	})

	sess, err := b.Create(backend.CreateOpts{
		Name:    "callback-test",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	select {
	case got := <-received:
		if got.ID != sess.ID {
			t.Fatalf("callback session ID mismatch: %q != %q", got.ID, sess.ID)
		}
		if got.Name != "callback-test" {
			t.Fatalf("callback session name mismatch: %q != %q", got.Name, "callback-test")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnSessionCreated callback")
	}
}

func TestNativeOnSessionEnded(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	received := make(chan string, 1)
	b.OnSessionEnded(func(id string) {
		select {
		case received <- id:
		default:
		}
	})

	sess, err := b.Create(backend.CreateOpts{
		Command: "sleep 60",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := b.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	select {
	case got := <-received:
		if got != sess.ID {
			t.Fatalf("ended callback ID mismatch: %q != %q", got, sess.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnSessionEnded callback")
	}
}

func TestNativeAutoNaming(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if !strings.HasPrefix(sess.Name, "session-") {
		t.Fatalf("expected auto-generated name to start with 'session-', got %q", sess.Name)
	}
}

func TestNativeResizeUnknown(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	err := b.Resize("nonexistent-id", 80, 24)
	if err == nil {
		t.Fatal("expected error when resizing nonexistent session")
	}
}

func TestNativeRenameUnknown(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	err := b.Rename("nonexistent-id", "new-name")
	if err == nil {
		t.Fatal("expected error when renaming nonexistent session")
	}
}

func TestNativeAttachUnknown(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	_, err := b.Attach("nonexistent-id")
	if err == nil {
		t.Fatal("expected error when attaching to nonexistent session")
	}
}

func TestNativeKillUnknown(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	err := b.Kill("nonexistent-id")
	if err != nil {
		t.Fatalf("expected Kill on nonexistent session to be idempotent (no error), got: %v", err)
	}
}

func TestNativeStreamOffData(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Command: testInfiniteEchoCommand(),
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	sh, err := b.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer sh.Close()

	// Register a listener and wait for at least one data event
	gotData := make(chan struct{}, 1)
	listenerID := sh.OnData(func(data []byte) int {
		select {
		case gotData <- struct{}{}:
		default:
		}
		return 0
	})

	select {
	case <-gotData:
		// Got at least one data event - listener is working
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial data from listener")
	}

	// Now remove the listener
	sh.OffData(listenerID)

	// Drain any buffered signals
	for {
		select {
		case <-gotData:
			continue
		default:
		}
		break
	}

	// Wait a bit and verify no more data arrives to the removed listener
	// We re-register a second listener to confirm data IS still flowing
	stillFlowing := make(chan struct{}, 1)
	sh.OnData(func(data []byte) int {
		select {
		case stillFlowing <- struct{}{}:
		default:
		}
		return 0
	})

	select {
	case <-stillFlowing:
		// Data is still flowing on the stream
	case <-time.After(5 * time.Second):
		t.Fatal("timed out - data stopped flowing entirely")
	}

	// The removed listener should not have received anything after OffData
	select {
	case <-gotData:
		t.Fatal("removed listener received data after OffData")
	default:
		// success - no data on the removed listener
	}
}

func TestNativeScrollbackContent(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	marker := "UNIQUE-MARKER-12345"
	sess, err := b.Create(backend.CreateOpts{
		Command: "echo " + marker + "; sleep 30",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			sb := b.GetScrollback(sess.ID)
			t.Fatalf("timed out waiting for scrollback to contain %q; current scrollback (%d bytes): %q", marker, len(sb), sb)
		default:
		}
		sb := b.GetScrollback(sess.ID)
		if strings.Contains(sb, marker) {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNativeShutdown(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)

	for i := 0; i < 3; i++ {
		_, err := b.Create(backend.CreateOpts{
			Command: "sleep 60",
		})
		if err != nil {
			t.Fatalf("Create %d failed: %v", i, err)
		}
	}

	list := b.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 sessions before shutdown, got %d", len(list))
	}

	// Shutdown closes connections and stops holders without removing sessions
	// from the process map (session metadata persists for daemon restart recovery).
	b.Shutdown()
}

// TestNativeStreamHandleCloseNoGoroutineLeak verifies that calling Close() on
// a StreamHandle terminates the forward() goroutine and does not leak it.
func TestNativeStreamHandleCloseNoGoroutineLeak(t *testing.T) {
	b := backend.NewNativeBackend(shortTempDir(t), false)
	defer b.Shutdown()

	sess, err := b.Create(backend.CreateOpts{
		Command: testInfiniteEchoCommand(),
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	sh, err := b.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}

	// Register a listener so the forward() goroutine is started.
	ready := make(chan struct{}, 1)
	sh.OnData(func(data []byte) int {
		select {
		case ready <- struct{}{}:
		default:
		}
		return 0
	})

	// Wait until we know data is flowing (forward() goroutine is running).
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for data before Close")
	}

	// Snapshot goroutine count before closing.
	before := runtime.NumGoroutine()

	// Close the handle - this should close h.ch and let forward() exit.
	sh.Close()

	// The forward() goroutine should exit shortly after Close(). Allow up
	// to 2 seconds for the runtime to schedule the goroutine exit.
	if !waitForGoroutineCount(before-1, 2*time.Second) {
		after := runtime.NumGoroutine()
		// A strict delta check: goroutine count should have dropped by at
		// least 1 (the forward goroutine). If it's the same or higher, leak.
		if after >= before {
			t.Fatalf("goroutine leak detected: count before Close=%d, after=%d (forward goroutine did not exit)", before, after)
		}
	}
}
