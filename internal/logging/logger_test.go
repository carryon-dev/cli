package logging

import "testing"

func TestLoggerFiltersByLevel(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "warn")

	logger.Debug("test", "debug message")
	logger.Info("test", "info message")
	logger.Warn("test", "warn message")
	logger.Error("test", "error message")

	entries := store.GetRecent(100)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Level != "warn" {
		t.Fatalf("expected first entry level warn, got %s", entries[0].Level)
	}
	if entries[1].Level != "error" {
		t.Fatalf("expected second entry level error, got %s", entries[1].Level)
	}
}

func TestLoggerDefaultLevel(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "info")

	logger.Debug("test", "debug message")
	logger.Info("test", "info message")

	entries := store.GetRecent(100)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Level != "info" {
		t.Fatalf("expected entry level info, got %s", entries[0].Level)
	}
}

func TestLoggerSetsComponent(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "debug")

	logger.Info("mycomp", "hello")

	entries := store.GetRecent(1)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Component != "mycomp" {
		t.Fatalf("expected component mycomp, got %s", entries[0].Component)
	}
	if entries[0].Message != "hello" {
		t.Fatalf("expected message hello, got %s", entries[0].Message)
	}
}

func TestLoggerSetsTimestamp(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "debug")

	logger.Info("test", "msg")

	entries := store.GetRecent(1)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Timestamp <= 0 {
		t.Fatal("expected positive timestamp")
	}
}

func TestErrorAndWarnAlwaysLogAtInfoLevel(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "info")

	logger.Error("test", "error message")
	logger.Warn("test", "warn message")

	entries := store.GetRecent(100)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries at info level, got %d", len(entries))
	}
	if entries[0].Level != "error" {
		t.Fatalf("expected first entry level error, got %s", entries[0].Level)
	}
	if entries[1].Level != "warn" {
		t.Fatalf("expected second entry level warn, got %s", entries[1].Level)
	}
}

func TestLoggerSetLevel(t *testing.T) {
	store := NewStore("", 5)
	logger := NewLogger(store, "error")

	logger.Warn("test", "should be filtered")
	if len(store.GetRecent(100)) != 0 {
		t.Fatal("expected warn to be filtered at error level")
	}

	logger.SetLevel("warn")
	logger.Warn("test", "should pass")
	if len(store.GetRecent(100)) != 1 {
		t.Fatal("expected warn to pass after SetLevel")
	}
}
