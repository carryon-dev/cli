package session_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/session"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "cos-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestManagerCreateAndList(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	sess, err := mgr.Create(backend.CreateOpts{
		Name:    "mgr-test",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("session ID mismatch")
	}

	got := mgr.Get(sess.ID)
	if got == nil {
		t.Fatal("expected Get to return session")
	}
	if got.Name != "mgr-test" {
		t.Fatalf("expected name 'mgr-test', got %q", got.Name)
	}
}

func TestManagerKill(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	sess, err := mgr.Create(backend.CreateOpts{
		Command: "sleep 60",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := mgr.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Wait for process exit cleanup
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session removal after kill")
		default:
		}
		list := mgr.List()
		if len(list) == 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestManagerGetByID(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	sess, err := mgr.Create(backend.CreateOpts{
		Name:    "get-test",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get existing session by ID
	got := mgr.Get(sess.ID)
	if got == nil {
		t.Fatal("expected Get to return session, got nil")
	}
	if got.ID != sess.ID {
		t.Fatalf("Get returned wrong session: %q != %q", got.ID, sess.ID)
	}
	if got.Name != "get-test" {
		t.Fatalf("expected name 'get-test', got %q", got.Name)
	}

	// Get nonexistent session
	missing := mgr.Get("nonexistent-id")
	if missing != nil {
		t.Fatalf("expected Get for nonexistent ID to return nil, got %+v", missing)
	}
}

func TestManagerRename(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	sess, err := mgr.Create(backend.CreateOpts{
		Name:    "before-rename",
		Command: "sleep 10",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := mgr.Rename(sess.ID, "after-rename"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	got := mgr.Get(sess.ID)
	if got == nil {
		t.Fatal("session not found after rename")
	}
	if got.Name != "after-rename" {
		t.Fatalf("expected name 'after-rename', got %q", got.Name)
	}
}

func TestManagerCreateWithBackend(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	sess, err := mgr.Create(backend.CreateOpts{
		Command: "sleep 10",
		Backend: "native",
	})
	if err != nil {
		t.Fatalf("Create with backend='native' failed: %v", err)
	}
	if sess.Backend != "native" {
		t.Fatalf("expected backend 'native', got %q", sess.Backend)
	}
}

func TestManagerCreateUnknownBackend(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	_, err := mgr.Create(backend.CreateOpts{
		Command: "sleep 10",
		Backend: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error when creating session with unknown backend")
	}
}

func TestManagerListSortedAlphabetically(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	// Create sessions in non-alphabetical order
	names := []string{"zeta", "alpha", "mike", "Beta"}
	for _, name := range names {
		_, err := mgr.Create(backend.CreateOpts{
			Name:    name,
			Command: "sleep 10",
		})
		if err != nil {
			t.Fatalf("Create %q failed: %v", name, err)
		}
	}

	list := mgr.List()
	if len(list) != 4 {
		t.Fatalf("expected 4 sessions, got %d", len(list))
	}

	// Should be case-insensitive alphabetical
	expected := []string{"alpha", "Beta", "mike", "zeta"}
	for i, want := range expected {
		if list[i].Name != want {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, want)
		}
	}
}

func TestManagerOnSessionCreated(t *testing.T) {
	reg := backend.NewRegistry()
	reg.Register(backend.NewNativeBackend(shortTempDir(t), false))

	mgr := session.NewManager(reg, "")
	defer mgr.Shutdown()

	received := make(chan backend.Session, 1)
	mgr.OnSessionCreated(func(sess backend.Session) {
		select {
		case received <- sess:
		default:
		}
	})

	sess, err := mgr.Create(backend.CreateOpts{
		Name:    "mgr-callback-test",
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
		if got.Name != "mgr-callback-test" {
			t.Fatalf("callback session name mismatch: %q != %q", got.Name, "mgr-callback-test")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for OnSessionCreated callback on manager")
	}
}
