package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
)

func TestSessionState_SaveAndGetAll(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	s := backend.Session{
		ID:      "sess-1",
		Name:    "dev",
		Backend: "native",
		Created: 1711000000000,
	}
	ss.Save(s)

	all := ss.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 session, got %d", len(all))
	}
	if all[0].ID != "sess-1" {
		t.Fatalf("expected session ID 'sess-1', got %q", all[0].ID)
	}
	if all[0].Name != "dev" {
		t.Fatalf("expected session name 'dev', got %q", all[0].Name)
	}
}

func TestSessionState_SaveAndGetByID(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	s := backend.Session{
		ID:      "sess-2",
		Name:    "build",
		Backend: "tmux",
		Created: 1711000000000,
	}
	ss.Save(s)

	got := ss.Get("sess-2")
	if got == nil {
		t.Fatal("expected to find session sess-2")
	}
	if got.Name != "build" {
		t.Fatalf("expected name 'build', got %q", got.Name)
	}

	missing := ss.Get("nonexistent")
	if missing != nil {
		t.Fatal("expected nil for nonexistent session")
	}
}

func TestSessionState_Remove(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	ss.Save(backend.Session{ID: "sess-3", Name: "a", Backend: "native", Created: 1})
	ss.Save(backend.Session{ID: "sess-4", Name: "b", Backend: "native", Created: 2})

	ss.Remove("sess-3")

	if ss.Get("sess-3") != nil {
		t.Fatal("expected sess-3 to be removed")
	}
	if ss.Get("sess-4") == nil {
		t.Fatal("expected sess-4 to still exist")
	}
	if len(ss.GetAll()) != 1 {
		t.Fatalf("expected 1 session after remove, got %d", len(ss.GetAll()))
	}
}

func TestSessionState_Persistence(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	ss.Save(backend.Session{
		ID:      "sess-5",
		Name:    "persist-test",
		Backend: "native",
		Created: 1711000000000,
		Cwd:     "/home/user",
	})
	ss.Flush()

	// Create a new SessionState from the same directory
	ss2 := NewSessionState(dir)
	got := ss2.Get("sess-5")
	if got == nil {
		t.Fatal("expected session to persist across instances")
	}
	if got.Name != "persist-test" {
		t.Fatalf("expected name 'persist-test', got %q", got.Name)
	}
	if got.Cwd != "/home/user" {
		t.Fatalf("expected cwd '/home/user', got %q", got.Cwd)
	}
}

func TestSessionState_LastAttachedPersists(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	ss.Save(backend.Session{ID: "sess-attach", Name: "dev", Backend: "native", Created: 1})

	// Simulate what the daemon does on attach: save with updated LastAttached
	updated := backend.Session{ID: "sess-attach", Name: "dev", Backend: "native", Created: 1, LastAttached: 1711707600000}
	ss.Save(updated)
	ss.Flush()

	// Reload from disk (simulates daemon restart)
	ss2 := NewSessionState(dir)
	got := ss2.Get("sess-attach")
	if got == nil {
		t.Fatal("expected session to persist after attach update")
	}
	if got.LastAttached != 1711707600000 {
		t.Fatalf("expected LastAttached 1711707600000 after reload, got %d", got.LastAttached)
	}
}

func TestSessionState_HandlesMissingAndCorruptFile(t *testing.T) {
	// Missing directory - should not error
	dir := filepath.Join(t.TempDir(), "nonexistent")
	ss := NewSessionState(dir)
	if len(ss.GetAll()) != 0 {
		t.Fatal("expected empty sessions for missing dir")
	}

	// Save should create the dir
	ss.Save(backend.Session{ID: "sess-6", Name: "x", Backend: "native", Created: 1})
	if ss.Get("sess-6") == nil {
		t.Fatal("expected session to be saved despite missing dir")
	}

	// Corrupt file - should start fresh
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, "sessions.json"), []byte("not json{{{"), 0644)
	ss2 := NewSessionState(dir2)
	if len(ss2.GetAll()) != 0 {
		t.Fatal("expected empty sessions for corrupt file")
	}
}

func TestSessionState_RenamePersists(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	ss.Save(backend.Session{ID: "sess-rename", Name: "original", Backend: "native", Created: 1})

	// Simulate what the daemon does on rename: save with updated name
	updated := backend.Session{ID: "sess-rename", Name: "renamed", Backend: "native", Created: 1}
	ss.Save(updated)
	ss.Flush()

	// Reload from disk (simulates daemon restart)
	ss2 := NewSessionState(dir)
	got := ss2.Get("sess-rename")
	if got == nil {
		t.Fatal("expected session to persist after rename")
	}
	if got.Name != "renamed" {
		t.Fatalf("expected name 'renamed' after reload, got %q", got.Name)
	}
}

func TestSessionState_SaveUpserts(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionState(dir)

	ss.Save(backend.Session{ID: "sess-7", Name: "original", Backend: "native", Created: 1})
	ss.Save(backend.Session{ID: "sess-7", Name: "updated", Backend: "native", Created: 1})

	all := ss.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 session after upsert, got %d", len(all))
	}
	if all[0].Name != "updated" {
		t.Fatalf("expected name 'updated', got %q", all[0].Name)
	}
}
