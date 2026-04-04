package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreGetRecentReturnsCorrectCount(t *testing.T) {
	store := NewStore("", 5)

	for i := 0; i < 20; i++ {
		store.Append(LogEntry{Timestamp: int64(i), Level: "info", Component: "test", Message: "msg"})
	}

	entries := store.GetRecent(5)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
	// Should be the last 5 entries (timestamps 15-19)
	if entries[0].Timestamp != 15 {
		t.Fatalf("expected first entry timestamp 15, got %d", entries[0].Timestamp)
	}
	if entries[4].Timestamp != 19 {
		t.Fatalf("expected last entry timestamp 19, got %d", entries[4].Timestamp)
	}
}

func TestStoreGetRecentMoreThanBuffer(t *testing.T) {
	store := NewStore("", 5)

	for i := 0; i < 3; i++ {
		store.Append(LogEntry{Timestamp: int64(i), Level: "info", Component: "test", Message: "msg"})
	}

	entries := store.GetRecent(100)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestStoreBufferCapsAtMaxSize(t *testing.T) {
	store := NewStoreWithBufferSize("", 5, 100)

	for i := 0; i < 150; i++ {
		store.Append(LogEntry{Timestamp: int64(i), Level: "info", Component: "test", Message: "msg"})
	}

	entries := store.GetRecent(200)
	if len(entries) != 100 {
		t.Fatalf("expected 100 entries (maxBufferSize), got %d", len(entries))
	}
	// Oldest entry should be #50 (first 50 were dropped)
	if entries[0].Timestamp != 50 {
		t.Fatalf("expected oldest entry timestamp 50, got %d", entries[0].Timestamp)
	}
}

func TestStoreCreatesLogDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "sub", "deep")

	store := NewStore(nested, 5)
	defer store.Close()

	// Verify the directory was created
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("expected log dir to be created, got error: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected log dir to be a directory")
	}

	// Verify log file works
	store.Append(LogEntry{Timestamp: 1, Level: "info", Component: "test", Message: "hello"})
	data, err := os.ReadFile(filepath.Join(nested, "daemon.log"))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("log file does not contain expected message, got: %s", string(data))
	}
}

func TestStoreRotationShiftsNumberedFiles(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "daemon.log")
	bigData := strings.Repeat("x", 11*1024*1024)
	if err := os.WriteFile(logFile, []byte(bigData), 0644); err != nil {
		t.Fatalf("failed to write big log file: %v", err)
	}

	// Create existing numbered files
	if err := os.WriteFile(filepath.Join(dir, "daemon.log.1"), []byte("content-one"), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.log.2"), []byte("content-two"), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	store := NewStore(dir, 5)
	defer store.Close()

	// After rotation: old daemon.log -> .1, old .1 -> .2, old .2 -> .3
	// Verify .1 now has the big data (was daemon.log)
	data1, err := os.ReadFile(filepath.Join(dir, "daemon.log.1"))
	if err != nil {
		t.Fatalf("failed to read daemon.log.1: %v", err)
	}
	if len(data1) != 11*1024*1024 {
		t.Fatalf("daemon.log.1 should be 11MB (rotated from daemon.log), got %d bytes", len(data1))
	}

	// Verify .2 now has content-one (was .1)
	data2, err := os.ReadFile(filepath.Join(dir, "daemon.log.2"))
	if err != nil {
		t.Fatalf("failed to read daemon.log.2: %v", err)
	}
	if string(data2) != "content-one" {
		t.Fatalf("daemon.log.2 should contain 'content-one' (shifted from .1), got: %s", string(data2))
	}

	// Verify .3 now has content-two (was .2)
	data3, err := os.ReadFile(filepath.Join(dir, "daemon.log.3"))
	if err != nil {
		t.Fatalf("failed to read daemon.log.3: %v", err)
	}
	if string(data3) != "content-two" {
		t.Fatalf("daemon.log.3 should contain 'content-two' (shifted from .2), got: %s", string(data3))
	}
}

func TestStoreCloseMultipleTimes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 5)

	store.Append(LogEntry{Timestamp: 1, Level: "info", Component: "test", Message: "msg"})

	// Close twice - should not panic
	store.Close()
	store.Close()
}

func TestStoreWritesToFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 5)
	defer store.Close()

	store.Append(LogEntry{Timestamp: 1000, Level: "info", Component: "test", Message: "hello"})

	// Read the log file
	data, err := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"message":"hello"`) {
		t.Fatalf("log file does not contain expected message, got: %s", content)
	}
	if !strings.Contains(content, `"level":"info"`) {
		t.Fatalf("log file does not contain expected level, got: %s", content)
	}
}

func TestStoreSubscriptionReceivesEntries(t *testing.T) {
	store := NewStore("", 5)

	var mu sync.Mutex
	var received []LogEntry

	unsub := store.Subscribe(func(entry LogEntry) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, entry)
	})

	store.Append(LogEntry{Timestamp: 1, Level: "info", Component: "test", Message: "first"})
	store.Append(LogEntry{Timestamp: 2, Level: "warn", Component: "test", Message: "second"})

	// Append calls subscribers synchronously, but poll for robustness
	var count int
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count = len(received)
		mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if count != 2 {
		t.Fatalf("expected 2 received entries, got %d", count)
	}

	// After unsubscribe, no more entries
	unsub()
	store.Append(LogEntry{Timestamp: 3, Level: "error", Component: "test", Message: "third"})

	mu.Lock()
	count = len(received)
	mu.Unlock()

	if count != 2 {
		t.Fatalf("expected still 2 received entries after unsub, got %d", count)
	}
}

func TestStoreRotation(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "daemon.log")

	// Create a file > 10MB
	bigData := strings.Repeat("x", 11*1024*1024)
	if err := os.WriteFile(logFile, []byte(bigData), 0644); err != nil {
		t.Fatalf("failed to write big log file: %v", err)
	}

	// Create an existing .1 file to verify it gets shifted
	if err := os.WriteFile(filepath.Join(dir, "daemon.log.1"), []byte("old-rotated"), 0644); err != nil {
		t.Fatalf("failed to write rotated file: %v", err)
	}

	store := NewStore(dir, 5)
	defer store.Close()

	// Original big file should have been rotated to .1
	if _, err := os.Stat(filepath.Join(dir, "daemon.log.1")); os.IsNotExist(err) {
		t.Fatal("daemon.log.1 should exist after rotation")
	}

	// The old .1 should have been shifted to .2
	if _, err := os.Stat(filepath.Join(dir, "daemon.log.2")); os.IsNotExist(err) {
		t.Fatal("daemon.log.2 should exist after rotation (shifted from .1)")
	}

	// The .1 file should now contain the big data
	data, err := os.ReadFile(filepath.Join(dir, "daemon.log.1"))
	if err != nil {
		t.Fatalf("failed to read rotated file: %v", err)
	}
	if len(data) != 11*1024*1024 {
		t.Fatalf("rotated .1 file should be 11MB, got %d bytes", len(data))
	}

	// The .2 file should contain old rotated content
	data2, err := os.ReadFile(filepath.Join(dir, "daemon.log.2"))
	if err != nil {
		t.Fatalf("failed to read shifted file: %v", err)
	}
	if string(data2) != "old-rotated" {
		t.Fatalf("shifted .2 file should contain old-rotated, got: %s", string(data2))
	}
}

func TestStoreRotationDeletesOldest(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "daemon.log")
	bigData := strings.Repeat("x", 11*1024*1024)
	if err := os.WriteFile(logFile, []byte(bigData), 0644); err != nil {
		t.Fatalf("failed to write big log file: %v", err)
	}

	// maxFiles=3 means we keep daemon.log, .1, .2 - the file at position .3 and beyond gets deleted
	// Create files at positions .1 and .2
	if err := os.WriteFile(filepath.Join(dir, "daemon.log.1"), []byte("one"), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.log.2"), []byte("two"), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	store := NewStore(dir, 3)
	defer store.Close()

	// After rotation: daemon.log -> .1, old .1 -> .2, old .2 should be deleted (would be .3 which >= maxFiles=3)
	if _, err := os.Stat(filepath.Join(dir, "daemon.log.1")); os.IsNotExist(err) {
		t.Fatal("daemon.log.1 should exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon.log.2")); os.IsNotExist(err) {
		t.Fatal("daemon.log.2 should exist")
	}
	// .3 should not exist (deleted because it exceeds maxFiles)
	if _, err := os.Stat(filepath.Join(dir, "daemon.log.3")); !os.IsNotExist(err) {
		t.Fatal("daemon.log.3 should NOT exist (exceeds maxFiles)")
	}
}

func TestStoreClose(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 5)

	store.Append(LogEntry{Timestamp: 1, Level: "info", Component: "test", Message: "before close"})
	store.Close()

	// After close, writing should still buffer but not write to file
	store.Append(LogEntry{Timestamp: 2, Level: "info", Component: "test", Message: "after close"})

	data, err := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "after close") {
		t.Fatal("log file should not contain entries written after close")
	}
	if !strings.Contains(content, "before close") {
		t.Fatal("log file should contain entries written before close")
	}
}
