package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerDefaults(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	if mgr.GetString("default.backend") != "native" {
		t.Fatal("expected default backend 'native'")
	}
	if mgr.GetInt("local.port") != 8384 {
		t.Fatal("expected default port 8384")
	}
	if mgr.GetBool("local.enabled") != false {
		t.Fatal("expected local.enabled false")
	}
}

func TestManagerSetAndGet(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	err := mgr.Set("local.port", "9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr.GetInt("local.port") != 9000 {
		t.Fatal("expected port 9000 after set")
	}
}

func TestManagerPersistence(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	mgr.Set("local.port", "9000")

	mgr2 := NewManager(dir)
	if mgr2.GetInt("local.port") != 9000 {
		t.Fatal("expected port 9000 after reload")
	}
}

func TestManagerSetInvalid(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	err := mgr.Set("local.port", "abc")
	if err == nil {
		t.Fatal("expected error for non-numeric port")
	}
}

func TestManagerReload(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	mgr.Set("local.port", "9000")

	configPath := filepath.Join(dir, "config.json")
	os.WriteFile(configPath, []byte(`{"local.port": 7777}`), 0644)

	mgr.Reload()
	if mgr.GetInt("local.port") != 7777 {
		t.Fatalf("expected port 7777 after reload, got %d", mgr.GetInt("local.port"))
	}
}

func TestManagerSetWarning(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	err := mgr.Set("local.expose", "true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	warning := mgr.LastWarning()
	if warning == "" {
		t.Fatal("expected warning for expose=true")
	}
}
