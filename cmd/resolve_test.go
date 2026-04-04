package cmd

import (
	"testing"

	"github.com/fatih/color"
	"github.com/carryon-dev/cli/internal/backend"
)

func init() {
	color.NoColor = true
}

func TestMatchSessions_ExactName(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "aaa111", Name: "dev-server"}, Location: "local"},
		{Session: backend.Session{ID: "bbb222", Name: "api-debug"}, Location: "local"},
	}
	matches := matchSessions("dev-server", sessions)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].ID != "aaa111" {
		t.Errorf("expected aaa111, got %s", matches[0].ID)
	}
}

func TestMatchSessions_ExactID(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "aaa111", Name: "dev-server"}, Location: "local"},
		{Session: backend.Session{ID: "bbb222", Name: "api-debug"}, Location: "local"},
	}
	matches := matchSessions("bbb222", sessions)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].ID != "bbb222" {
		t.Errorf("expected bbb222, got %s", matches[0].ID)
	}
}

func TestMatchSessions_PrefixID(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "aaa111bbb222", Name: "dev"}, Location: "local"},
		{Session: backend.Session{ID: "ccc333ddd444", Name: "api"}, Location: "local"},
	}
	matches := matchSessions("aaa", sessions)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].ID != "aaa111bbb222" {
		t.Errorf("expected aaa111bbb222, got %s", matches[0].ID)
	}
}

func TestMatchSessions_MultipleNameMatches(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "aaa111", Name: "dev-server"}, Location: "local"},
		{Session: backend.Session{ID: "bbb222", Name: "dev-server"}, Location: "macbook"},
	}
	matches := matchSessions("dev-server", sessions)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
}

func TestMatchSessions_NoMatch(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "aaa111", Name: "dev-server"}, Location: "local"},
	}
	matches := matchSessions("nonexistent", sessions)
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestMatchSessions_NameTakesPriorityOverIDPrefix(t *testing.T) {
	sessions := []resolveCandidate{
		{Session: backend.Session{ID: "abc999xyz", Name: "other"}, Location: "local"},
		{Session: backend.Session{ID: "zzz000", Name: "abc"}, Location: "local"},
	}
	matches := matchSessions("abc", sessions)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match (exact name), got %d", len(matches))
	}
	if matches[0].ID != "zzz000" {
		t.Errorf("expected zzz000 (exact name match), got %s", matches[0].ID)
	}
}

func TestShowLocation_AllLocal(t *testing.T) {
	candidates := []resolveCandidate{
		{Session: backend.Session{ID: "aaa"}, Location: "local"},
		{Session: backend.Session{ID: "bbb"}, Location: "local"},
	}
	if showLocation(candidates) {
		t.Error("should not show location when all local")
	}
}

func TestShowLocation_MixedLocations(t *testing.T) {
	candidates := []resolveCandidate{
		{Session: backend.Session{ID: "aaa"}, Location: "local"},
		{Session: backend.Session{ID: "bbb"}, Location: "macbook"},
	}
	if !showLocation(candidates) {
		t.Error("should show location when mixed local/remote")
	}
}
