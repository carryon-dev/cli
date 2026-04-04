package project

import (
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
)

func TestMatchTerminals_Running(t *testing.T) {
	declared := []DeclaredTerminal{
		{Name: "server"},
	}
	running := []backend.Session{
		{ID: "abc123", Name: "server", Backend: "native"},
	}

	matches := MatchTerminals(declared, running)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Status != "running" {
		t.Errorf("expected status 'running', got %q", matches[0].Status)
	}
	if matches[0].Session == nil {
		t.Fatal("expected session to be set")
	}
	if matches[0].Session.ID != "abc123" {
		t.Errorf("expected session ID 'abc123', got %q", matches[0].Session.ID)
	}
}

func TestMatchTerminals_Missing(t *testing.T) {
	declared := []DeclaredTerminal{
		{Name: "server"},
	}
	running := []backend.Session{}

	matches := MatchTerminals(declared, running)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Status != "missing" {
		t.Errorf("expected status 'missing', got %q", matches[0].Status)
	}
	if matches[0].Session != nil {
		t.Error("expected session to be nil for missing terminal")
	}
}

func TestMatchTerminals_Mixed(t *testing.T) {
	declared := []DeclaredTerminal{
		{Name: "server"},
		{Name: "worker"},
		{Name: "watcher"},
	}
	running := []backend.Session{
		{ID: "s1", Name: "server", Backend: "native"},
		{ID: "s2", Name: "watcher", Backend: "tmux"},
		{ID: "s3", Name: "other", Backend: "native"},
	}

	matches := MatchTerminals(declared, running)
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(matches))
	}

	// server → running
	if matches[0].Declared.Name != "server" {
		t.Errorf("expected declared name 'server', got %q", matches[0].Declared.Name)
	}
	if matches[0].Status != "running" {
		t.Errorf("expected 'running' for server, got %q", matches[0].Status)
	}
	if matches[0].Session == nil || matches[0].Session.ID != "s1" {
		t.Error("expected session s1 for server")
	}

	// worker → missing
	if matches[1].Declared.Name != "worker" {
		t.Errorf("expected declared name 'worker', got %q", matches[1].Declared.Name)
	}
	if matches[1].Status != "missing" {
		t.Errorf("expected 'missing' for worker, got %q", matches[1].Status)
	}
	if matches[1].Session != nil {
		t.Error("expected nil session for worker")
	}

	// watcher → running
	if matches[2].Declared.Name != "watcher" {
		t.Errorf("expected declared name 'watcher', got %q", matches[2].Declared.Name)
	}
	if matches[2].Status != "running" {
		t.Errorf("expected 'running' for watcher, got %q", matches[2].Status)
	}
	if matches[2].Session == nil || matches[2].Session.ID != "s2" {
		t.Error("expected session s2 for watcher")
	}
}
