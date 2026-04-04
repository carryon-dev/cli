package backend_test

import (
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
)

// stubBackend implements backend.Backend for testing the registry.
type stubBackend struct {
	id        string
	available bool
}

func (s *stubBackend) ID() string        { return s.id }
func (s *stubBackend) Available() bool    { return s.available }
func (s *stubBackend) List() []backend.Session { return nil }
func (s *stubBackend) Create(_ backend.CreateOpts) (backend.Session, error) {
	return backend.Session{}, nil
}
func (s *stubBackend) Attach(_ string) (backend.StreamHandle, error) { return nil, nil }
func (s *stubBackend) Resize(_ string, _, _ uint16) error            { return nil }
func (s *stubBackend) Rename(_ string, _ string) error               { return nil }
func (s *stubBackend) GetScrollback(_ string) string                 { return "" }
func (s *stubBackend) Kill(_ string) error                           { return nil }
func (s *stubBackend) OnSessionCreated(_ func(backend.Session))      {}
func (s *stubBackend) OnSessionEnded(_ func(string))                 {}
func (s *stubBackend) OnOutput(_ func(string, []byte))               {}
func (s *stubBackend) Shutdown()                                     {}

func TestRegisterAndGet(t *testing.T) {
	r := backend.NewRegistry()
	b := &stubBackend{id: "test", available: true}
	r.Register(b)

	got := r.Get("test")
	if got == nil {
		t.Fatal("expected to get registered backend")
	}
	if got.ID() != "test" {
		t.Fatalf("expected id 'test', got %q", got.ID())
	}

	if r.Get("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent backend")
	}
}

func TestGetDefaultWithPreferredID(t *testing.T) {
	r := backend.NewRegistry()
	native := &stubBackend{id: "native", available: true}
	tmux := &stubBackend{id: "tmux", available: true}
	r.Register(native)
	r.Register(tmux)

	got := r.GetDefault("tmux")
	if got == nil || got.ID() != "tmux" {
		t.Fatalf("expected tmux backend, got %v", got)
	}
}

func TestGetDefaultFallbackToNative(t *testing.T) {
	r := backend.NewRegistry()
	native := &stubBackend{id: "native", available: true}
	tmux := &stubBackend{id: "tmux", available: false}
	r.Register(native)
	r.Register(tmux)

	// Preferred is unavailable, should fall back to "native"
	got := r.GetDefault("tmux")
	if got == nil || got.ID() != "native" {
		t.Fatalf("expected native fallback, got %v", got)
	}

	// No preferred, should return "native"
	got = r.GetDefault("")
	if got == nil || got.ID() != "native" {
		t.Fatalf("expected native as default, got %v", got)
	}
}

func TestGetAvailable(t *testing.T) {
	r := backend.NewRegistry()
	r.Register(&stubBackend{id: "a", available: true})
	r.Register(&stubBackend{id: "b", available: false})
	r.Register(&stubBackend{id: "c", available: true})

	avail := r.GetAvailable()
	if len(avail) != 2 {
		t.Fatalf("expected 2 available backends, got %d", len(avail))
	}
}

func TestGetAll(t *testing.T) {
	r := backend.NewRegistry()
	r.Register(&stubBackend{id: "a", available: true})
	r.Register(&stubBackend{id: "b", available: false})

	all := r.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(all))
	}
}
