package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LogEntry represents a single log record.
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

// LogSubscriber is a callback that receives log entries.
type LogSubscriber func(entry LogEntry)

const defaultMaxBufferSize = 10000

// Store holds an in-memory ring buffer of log entries, optionally writing
// JSON-lines to a rotating log file and notifying subscribers.
type Store struct {
	mu            sync.Mutex
	buffer        []LogEntry
	maxBufferSize int
	logDir        string
	logFile       *os.File
	maxFiles      int
	subscribers   map[int]LogSubscriber
	nextSubID     int
}

// NewStore creates a Store. If logDir is non-empty, log entries are also
// written to {logDir}/daemon.log with rotation when the file exceeds 10MB.
func NewStore(logDir string, maxFiles int) *Store {
	return NewStoreWithBufferSize(logDir, maxFiles, defaultMaxBufferSize)
}

// NewStoreWithBufferSize creates a Store with a custom in-memory buffer cap.
func NewStoreWithBufferSize(logDir string, maxFiles int, maxBufferSize int) *Store {
	s := &Store{
		maxBufferSize: maxBufferSize,
		logDir:        logDir,
		maxFiles:      maxFiles,
		subscribers:   make(map[int]LogSubscriber),
	}
	if logDir != "" {
		_ = os.MkdirAll(logDir, 0700)
		s.rotateIfNeeded()
		f, err := os.OpenFile(filepath.Join(logDir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			s.logFile = f
		}
	}
	return s
}

// Append adds an entry to the in-memory buffer, writes it to the log file
// (if configured), and notifies all subscribers.
func (s *Store) Append(entry LogEntry) {
	s.mu.Lock()

	s.buffer = append(s.buffer, entry)
	if len(s.buffer) > s.maxBufferSize {
		// Drop oldest entries to stay at maxBufferSize.
		excess := len(s.buffer) - s.maxBufferSize
		s.buffer = s.buffer[excess:]
	}

	// Snapshot log file handle and subscribers so we can use them outside the lock.
	logFile := s.logFile
	subs := make([]LogSubscriber, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	// Write to log file outside the main lock to avoid blocking concurrent callers on disk I/O.
	if logFile != nil {
		data, err := json.Marshal(entry)
		if err == nil {
			_, writeErr := logFile.Write(append(data, '\n'))
			if writeErr != nil {
				// Use the snapshot (logFile) not the field (s.logFile) to avoid
				// a nil deref if Close() races and sets s.logFile = nil.
				failedPath := logFile.Name()
				logFile.Close()
				s.mu.Lock()
				if s.logFile == logFile {
					s.logFile = nil
				}
				s.mu.Unlock()
				fmt.Fprintf(os.Stderr, "[carryon] log file write failed (%s): %s\n", failedPath, writeErr)
			}
		}
	}

	for _, sub := range subs {
		sub(entry)
	}
}

// GetRecent returns up to n of the most recent log entries.
func (s *Store) GetRecent(n int) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n >= len(s.buffer) {
		out := make([]LogEntry, len(s.buffer))
		copy(out, s.buffer)
		return out
	}
	start := len(s.buffer) - n
	out := make([]LogEntry, n)
	copy(out, s.buffer[start:])
	return out
}

// Subscribe registers a callback that will be invoked for every new log entry.
// It returns an unsubscribe function.
func (s *Store) Subscribe(cb LogSubscriber) func() {
	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = cb
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()
	}
}

// GetRecentAndSubscribe atomically snapshots the most recent n entries and
// registers a subscriber. This closes the gap where entries could be written
// between a separate GetRecent and Subscribe call.
func (s *Store) GetRecentAndSubscribe(n int, cb LogSubscriber) ([]LogEntry, func()) {
	s.mu.Lock()

	// Snapshot entries.
	var entries []LogEntry
	if n >= len(s.buffer) {
		entries = make([]LogEntry, len(s.buffer))
		copy(entries, s.buffer)
	} else {
		start := len(s.buffer) - n
		entries = make([]LogEntry, n)
		copy(entries, s.buffer[start:])
	}

	// Register subscriber under the same lock.
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = cb

	s.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()
	}
	return entries, unsub
}

// Close closes the underlying log file (if any). In-memory buffering and
// subscriber notification continue to work after Close; only file writes stop.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
	}
}

// rotateIfNeeded rotates daemon.log if it exceeds 10MB.
func (s *Store) rotateIfNeeded() {
	if s.logDir == "" {
		return
	}

	logFile := filepath.Join(s.logDir, "daemon.log")
	info, err := os.Stat(logFile)
	if err != nil {
		return
	}
	if info.Size() < 10*1024*1024 {
		return
	}

	// Shift existing rotated files: .N -> .N+1  (from high to low)
	for i := s.maxFiles - 1; i >= 1; i-- {
		from := filepath.Join(s.logDir, fmt.Sprintf("daemon.log.%d", i))
		to := filepath.Join(s.logDir, fmt.Sprintf("daemon.log.%d", i+1))
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if i+1 >= s.maxFiles {
			os.Remove(from)
		} else {
			os.Rename(from, to)
		}
	}

	os.Rename(logFile, filepath.Join(s.logDir, "daemon.log.1"))
}
