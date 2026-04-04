package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProjectConfig_Valid(t *testing.T) {
	raw := `{
		"version": 1,
		"terminals": [
			{"name": "server", "command": "npm start", "cwd": "/app"},
			{"name": "worker", "backend": "tmux"}
		]
	}`

	cfg, err := ParseProjectConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}
	if len(cfg.Terminals) != 2 {
		t.Fatalf("expected 2 terminals, got %d", len(cfg.Terminals))
	}
	if cfg.Terminals[0].Single == nil {
		t.Fatal("expected Terminals[0].Single to be set")
	}
	if cfg.Terminals[0].Single.Name != "server" {
		t.Errorf("expected name 'server', got %q", cfg.Terminals[0].Single.Name)
	}
	if cfg.Terminals[0].Single.Command != "npm start" {
		t.Errorf("expected command 'npm start', got %q", cfg.Terminals[0].Single.Command)
	}
	if cfg.Terminals[0].Single.Cwd != "/app" {
		t.Errorf("expected cwd '/app', got %q", cfg.Terminals[0].Single.Cwd)
	}
	if cfg.Terminals[1].Single == nil {
		t.Fatal("expected Terminals[1].Single to be set")
	}
	if cfg.Terminals[1].Single.Name != "worker" {
		t.Errorf("expected name 'worker', got %q", cfg.Terminals[1].Single.Name)
	}
	if cfg.Terminals[1].Single.Backend != "tmux" {
		t.Errorf("expected backend 'tmux', got %q", cfg.Terminals[1].Single.Backend)
	}
}

func TestParseProjectConfig_MissingVersion(t *testing.T) {
	raw := `{"terminals": [{"name": "server"}]}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestParseProjectConfig_WrongVersion(t *testing.T) {
	raw := `{"version": 2, "terminals": [{"name": "server"}]}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for wrong version")
	}
}

func TestParseProjectConfig_MissingTerminals(t *testing.T) {
	raw := `{"version": 1}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for missing terminals")
	}
}

func TestParseProjectConfig_TerminalWithoutName(t *testing.T) {
	raw := `{"version": 1, "terminals": [{"command": "npm start"}]}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for terminal without name")
	}
}

func TestReadProjectConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	content := `{"version": 1, "terminals": [{"name": "dev"}]}`
	err := os.WriteFile(filepath.Join(dir, ".carryon.json"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg := ReadProjectConfig(dir)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Terminals) != 1 {
		t.Fatalf("expected 1 terminal, got %d", len(cfg.Terminals))
	}
	if cfg.Terminals[0].Single == nil {
		t.Fatal("expected Terminals[0].Single to be set")
	}
	if cfg.Terminals[0].Single.Name != "dev" {
		t.Errorf("expected name 'dev', got %q", cfg.Terminals[0].Single.Name)
	}
}

func TestReadProjectConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := ReadProjectConfig(dir)
	if cfg != nil {
		t.Fatal("expected nil config for missing file")
	}
}

func TestParseProjectConfig_SplitGroup(t *testing.T) {
	raw := `{
		"version": 1,
		"terminals": [
			{"name": "server", "command": "npm start"},
			[
				{"name": "left", "command": "watch -n1 date"},
				{"name": "right", "command": "top"}
			]
		]
	}`

	cfg, err := ParseProjectConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Terminals) != 2 {
		t.Fatalf("expected 2 terminal entries, got %d", len(cfg.Terminals))
	}

	// First entry is a single terminal
	if cfg.Terminals[0].Single == nil {
		t.Fatal("expected Terminals[0].Single to be set")
	}
	if cfg.Terminals[0].Single.Name != "server" {
		t.Errorf("expected name 'server', got %q", cfg.Terminals[0].Single.Name)
	}

	// Second entry is a split group
	if cfg.Terminals[1].Single != nil {
		t.Fatal("expected Terminals[1].Single to be nil for split group")
	}
	if len(cfg.Terminals[1].Group) != 2 {
		t.Fatalf("expected 2 terminals in split group, got %d", len(cfg.Terminals[1].Group))
	}
	if cfg.Terminals[1].Group[0].Name != "left" {
		t.Errorf("expected group[0] name 'left', got %q", cfg.Terminals[1].Group[0].Name)
	}
	if cfg.Terminals[1].Group[1].Name != "right" {
		t.Errorf("expected group[1] name 'right', got %q", cfg.Terminals[1].Group[1].Name)
	}
}

func TestParseProjectConfig_ColorField(t *testing.T) {
	raw := `{
		"version": 1,
		"terminals": [
			{"name": "server", "color": "#4ecdc4"},
			[
				{"name": "left", "color": "red"},
				{"name": "right"}
			]
		]
	}`

	cfg, err := ParseProjectConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Terminals[0].Single == nil {
		t.Fatal("expected Terminals[0].Single to be set")
	}
	if cfg.Terminals[0].Single.Color != "#4ecdc4" {
		t.Errorf("expected color '#4ecdc4', got %q", cfg.Terminals[0].Single.Color)
	}

	if len(cfg.Terminals[1].Group) != 2 {
		t.Fatalf("expected 2 terminals in split group, got %d", len(cfg.Terminals[1].Group))
	}
	if cfg.Terminals[1].Group[0].Color != "red" {
		t.Errorf("expected group[0] color 'red', got %q", cfg.Terminals[1].Group[0].Color)
	}
	if cfg.Terminals[1].Group[1].Color != "" {
		t.Errorf("expected group[1] color to be empty, got %q", cfg.Terminals[1].Group[1].Color)
	}
}

func TestValidateProjectConfig_EmptySplitGroup(t *testing.T) {
	raw := `{"version": 1, "terminals": [[]]}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for empty split group")
	}
}

func TestValidateProjectConfig_MissingNameInSplitGroup(t *testing.T) {
	raw := `{"version": 1, "terminals": [[{"command": "npm start"}]]}`
	_, err := ParseProjectConfig(raw)
	if err == nil {
		t.Fatal("expected error for missing name inside split group")
	}
}

func TestProjectConfig_AllTerminals(t *testing.T) {
	raw := `{
		"version": 1,
		"terminals": [
			{"name": "server"},
			[
				{"name": "left"},
				{"name": "right"}
			],
			{"name": "worker"}
		]
	}`

	cfg, err := ParseProjectConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := cfg.AllTerminals()
	if len(all) != 4 {
		t.Fatalf("expected 4 terminals from AllTerminals(), got %d", len(all))
	}

	names := []string{"server", "left", "right", "worker"}
	for i, expected := range names {
		if all[i].Name != expected {
			t.Errorf("AllTerminals()[%d]: expected name %q, got %q", i, expected, all[i].Name)
		}
	}
}
