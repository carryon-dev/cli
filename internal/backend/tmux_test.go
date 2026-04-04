//go:build !windows

package backend_test

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
)

func tmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func TestTmuxAvailability(t *testing.T) {
	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	if tmuxAvailable() {
		if !b.Available() {
			t.Fatal("tmux is on PATH but backend reports unavailable")
		}
	} else {
		if b.Available() {
			t.Fatal("tmux is not on PATH but backend reports available")
		}
	}
}

func TestTmuxCreateAndList(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{
		Name: name,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Clean up the tmux session when done
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	if sess.Name != name {
		t.Fatalf("expected name %q, got %q", name, sess.Name)
	}
	if sess.Backend != "tmux" {
		t.Fatalf("expected backend 'tmux', got %q", sess.Backend)
	}
	if !strings.HasPrefix(sess.ID, "tmux-") {
		t.Fatalf("expected ID to start with 'tmux-', got %q", sess.ID)
	}

	list := b.List()
	found := false
	for _, s := range list {
		if s.ID == sess.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created session %q not found in list (list has %d sessions)", sess.ID, len(list))
	}
}

func TestTmuxGetScrollback(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-sb-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{
		Name:    name,
		Command: "echo scrollback-tmux-test && sleep 30",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	// Wait for the command to produce output
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for scrollback data")
		default:
		}
		sb := b.GetScrollback(sess.ID)
		if strings.Contains(sb, "scrollback-tmux-test") {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTmuxKill(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-kill-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := b.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	list := b.List()
	for _, s := range list {
		if s.ID == sess.ID {
			t.Fatalf("session %q still found in list after Kill", sess.ID)
		}
	}
}

func TestTmuxRename(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-ren-%d", time.Now().UnixNano())
	newName := fmt.Sprintf("carryon-test-renamed-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill("tmux-" + newName)
	}()

	if err := b.Rename(sess.ID, newName); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	list := b.List()
	foundNew := false
	for _, s := range list {
		if s.Name == newName {
			foundNew = true
		}
		if s.Name == name {
			t.Fatalf("old name %q still found in list after Rename", name)
		}
	}
	if !foundNew {
		t.Fatalf("new name %q not found in list after Rename", newName)
	}
}

func TestTmuxCreateWithCommand(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-cmd-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{
		Name:    name,
		Command: "echo hello && sleep 30",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	if sess.Name != name {
		t.Fatalf("expected name %q, got %q", name, sess.Name)
	}

	list := b.List()
	found := false
	for _, s := range list {
		if s.ID == sess.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session created with command not found in list")
	}
}

func TestTmuxCreateWithCwd(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-cwd-%d", time.Now().UnixNano())

	sess, err := b.Create(backend.CreateOpts{
		Name: name,
		Cwd:  "/tmp",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	// The cwd is populated by syncSessions from tmux's session_path.
	// On macOS /tmp is a symlink to /private/tmp, so check both.
	list := b.List()
	for _, s := range list {
		if s.ID == sess.ID {
			if s.Cwd != "/tmp" && s.Cwd != "/private/tmp" {
				t.Fatalf("expected cwd '/tmp' or '/private/tmp', got %q", s.Cwd)
			}
			return
		}
	}
	t.Fatal("session not found in list")
}

func TestTmuxDiscoverExisting(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	name := fmt.Sprintf("carryon-test-ext-%d", time.Now().UnixNano())

	// Create a tmux session externally (not via the backend).
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external tmux new-session failed: %v (%s)", err, string(out))
	}
	defer func() {
		exec.Command("tmux", "kill-session", "-t", name).Run()
	}()

	// Now create the backend - it should discover the existing session.
	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	list := b.List()
	found := false
	for _, s := range list {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("externally created tmux session %q not found by backend (list has %d sessions)", name, len(list))
	}
}

func TestTmuxOnSessionCreated(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	var mu sync.Mutex
	var createdSess *backend.Session

	b.OnSessionCreated(func(s backend.Session) {
		mu.Lock()
		defer mu.Unlock()
		createdSess = &s
	})

	name := fmt.Sprintf("carryon-test-oncreate-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	mu.Lock()
	cs := createdSess
	mu.Unlock()

	if cs == nil {
		t.Fatal("OnSessionCreated callback was not fired")
	}
	if cs.Name != name {
		t.Fatalf("callback received wrong session name: expected %q, got %q", name, cs.Name)
	}
}

func TestTmuxOnSessionEnded(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	var mu sync.Mutex
	var endedID string

	b.OnSessionEnded(func(id string) {
		mu.Lock()
		defer mu.Unlock()
		endedID = id
	})

	name := fmt.Sprintf("carryon-test-onend-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := b.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	mu.Lock()
	eid := endedID
	mu.Unlock()

	if eid == "" {
		t.Fatal("OnSessionEnded callback was not fired")
	}
	if eid != sess.ID {
		t.Fatalf("callback received wrong session ID: expected %q, got %q", sess.ID, eid)
	}
}

func TestTmuxAttachReceiveOutput(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-attach-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	stream, err := b.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer stream.Close()

	// Collect output from the stream
	var mu sync.Mutex
	var collected []byte
	stream.OnData(func(data []byte) int {
		mu.Lock()
		collected = append(collected, data...)
		mu.Unlock()
		return 0
	})

	// Use tmux send-keys to type a command that produces identifiable output
	marker := fmt.Sprintf("MARKER-%d", time.Now().UnixNano())
	cmd := exec.Command("tmux", "send-keys", "-t", name, fmt.Sprintf("echo %s", marker), "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send-keys failed: %v (%s)", err, string(out))
	}

	// Wait for the marker to appear in the stream output
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			mu.Lock()
			got := string(collected)
			mu.Unlock()
			t.Fatalf("timed out waiting for marker in stream output (got %d bytes: %q)", len(got), got)
		default:
		}
		mu.Lock()
		got := string(collected)
		mu.Unlock()
		if strings.Contains(got, marker) {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTmuxResize(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-resize-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		_ = b.Kill(sess.ID)
	}()

	// Attach first so there's a PTY to resize
	stream, err := b.Attach(sess.ID)
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer stream.Close()

	if err := b.Resize(sess.ID, 120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestTmuxSyncDetectsRemoval(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	name := fmt.Sprintf("carryon-test-syncrem-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify it's in the list.
	list := b.List()
	found := false
	for _, s := range list {
		if s.ID == sess.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session not found in list after create")
	}

	// Kill the session externally using tmux directly.
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external tmux kill-session failed: %v (%s)", err, string(out))
	}

	// Wait for rate-limited sync to pick up the removal (sync interval: 1s).
	time.Sleep(1100 * time.Millisecond)
	list = b.List()
	for _, s := range list {
		if s.ID == sess.ID {
			t.Fatalf("session %q still in list after external kill", sess.ID)
		}
	}
}

func TestTmuxShutdownDoesNotKill(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	b := backend.NewTmuxBackend()

	name := fmt.Sprintf("carryon-test-shutnokill-%d", time.Now().UnixNano())
	sess, err := b.Create(backend.CreateOpts{Name: name})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() {
		// Clean up: kill the session via tmux directly
		exec.Command("tmux", "kill-session", "-t", name).Run()
	}()

	_ = sess // avoid unused

	// Shutdown the backend - this should NOT kill the tmux session.
	b.Shutdown()

	// Verify the tmux session still exists externally.
	cmd := exec.Command("tmux", "has-session", "-t", name)
	if err := cmd.Run(); err != nil {
		t.Fatalf("tmux session %q was killed by Shutdown, but it should persist", name)
	}
}

func TestTmuxUnavailable(t *testing.T) {
	// This test verifies behavior when tmux is not available.
	// We can't easily remove tmux from PATH in a Go test without
	// affecting other tests running in parallel, so we skip if
	// tmux IS available and just verify the code path in the
	// opposite case.
	if tmuxAvailable() {
		t.Skip("tmux is available; cannot test unavailable path without mocking PATH")
	}

	b := backend.NewTmuxBackend()
	defer b.Shutdown()

	if b.Available() {
		t.Fatal("expected Available() to return false when tmux is not on PATH")
	}

	list := b.List()
	if len(list) != 0 {
		t.Fatalf("expected empty list when tmux unavailable, got %d", len(list))
	}

	_, err := b.Create(backend.CreateOpts{Name: "test"})
	if err == nil {
		t.Fatal("expected Create to fail when tmux is unavailable")
	}
}
