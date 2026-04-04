package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/carryon-dev/cli/internal/backend"
)

func init() {
	// Disable color in tests so we can assert on plain text content.
	color.NoColor = true
}

func TestFormatSessionLine(t *testing.T) {
	now := time.Now()
	s := backend.Session{
		ID:              "abc123def456",
		Name:            "my-project",
		AttachedClients: 2,
		Created:         now.Add(-2 * time.Hour).UnixMilli(),
	}

	line := formatSessionLine(s)
	if !strings.Contains(line, "my-project") {
		t.Errorf("expected line to contain session name, got: %s", line)
	}
	if !strings.Contains(line, "2 clients") {
		t.Errorf("expected line to contain client count, got: %s", line)
	}
	if !strings.Contains(line, "2h ago") {
		t.Errorf("expected line to contain relative time, got: %s", line)
	}
	if !strings.Contains(line, "abc123def456") {
		t.Errorf("expected line to contain ID at the end, got: %s", line)
	}
}

func TestFormatSessionLineNoClients(t *testing.T) {
	now := time.Now()
	s := backend.Session{
		ID:              "xyz789",
		Name:            "idle",
		AttachedClients: 0,
		Created:         now.Add(-30 * time.Hour).UnixMilli(),
	}

	line := formatSessionLine(s)
	if !strings.Contains(line, "no clients") {
		t.Errorf("expected 'no clients', got: %s", line)
	}
	if !strings.Contains(line, "yesterday") {
		t.Errorf("expected 'yesterday', got: %s", line)
	}
}

func TestFormatSessionLines_Empty(t *testing.T) {
	result := formatSessionLines(nil)
	if !strings.Contains(result, "No sessions running.") {
		t.Errorf("expected empty state message, got: %s", result)
	}
	if !strings.Contains(result, "carryon") {
		t.Errorf("expected hint with 'carryon', got: %s", result)
	}
}

func TestFormatSessionLines_MultipleSessions(t *testing.T) {
	now := time.Now()
	sessions := []backend.Session{
		{ID: "aaa", Name: "first", AttachedClients: 1, Created: now.UnixMilli()},
		{ID: "bbb", Name: "second", AttachedClients: 0, Created: now.Add(-time.Hour).UnixMilli()},
	}
	result := formatSessionLines(sessions)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %s", len(lines), result)
	}
}

func TestColumnOrder_NameFirst_IDLast(t *testing.T) {
	now := time.Now()
	s := backend.Session{
		ID:              "abc123def456",
		Name:            "my-project",
		AttachedClients: 1,
		Created:         now.UnixMilli(),
	}

	line := formatSessionLine(s)
	nameIdx := strings.Index(line, "my-project")
	idIdx := strings.Index(line, "abc123def456")
	if nameIdx >= idIdx {
		t.Errorf("name should appear before ID in output: %s", line)
	}
}
