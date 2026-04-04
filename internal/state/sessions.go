package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
)

// SessionState stores sessions in memory and persists them as a JSON array.
type SessionState struct {
	mu         sync.Mutex
	sessions   map[string]backend.Session
	stateDir   string
	filePath   string
	dirty      bool
	flushTimer *time.Timer
}

// NewSessionState creates a SessionState, loading any existing data from disk.
func NewSessionState(stateDir string) *SessionState {
	ss := &SessionState{
		stateDir: stateDir,
		filePath: filepath.Join(stateDir, "sessions.json"),
		sessions: make(map[string]backend.Session),
	}
	ss.load()
	return ss
}

// GetAll returns all sessions as a slice.
func (ss *SessionState) GetAll() []backend.Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	result := make([]backend.Session, 0, len(ss.sessions))
	for _, s := range ss.sessions {
		result = append(result, s)
	}
	return result
}

// Get returns a pointer to the session with the given ID, or nil if not found.
func (ss *SessionState) Get(id string) *backend.Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.sessions[id]
	if !ok {
		return nil
	}
	return &s
}

// Save upserts a session and schedules a debounced persist to disk.
func (ss *SessionState) Save(session backend.Session) {
	ss.mu.Lock()
	ss.sessions[session.ID] = session
	ss.dirty = true
	if ss.flushTimer == nil {
		ss.flushTimer = time.AfterFunc(100*time.Millisecond, ss.flushIfDirty)
	}
	ss.mu.Unlock()
}

// Flush forces an immediate persist if dirty. This is useful before shutdown
// or when the caller needs the data on disk right away.
func (ss *SessionState) Flush() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.flushTimer != nil {
		ss.flushTimer.Stop()
		ss.flushTimer = nil
	}
	if ss.dirty {
		ss.persist()
		ss.dirty = false
	}
}

// flushIfDirty persists the state if it has been marked dirty.
func (ss *SessionState) flushIfDirty() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.flushTimer = nil
	if ss.dirty {
		ss.persist()
		ss.dirty = false
	}
}

// Remove deletes a session by ID and persists the state to disk.
func (ss *SessionState) Remove(id string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.sessions, id)
	ss.persist()
}

func (ss *SessionState) load() {
	data, err := os.ReadFile(ss.filePath)
	if err != nil {
		return
	}
	var sessions []backend.Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		// Corrupt JSON - start fresh
		return
	}
	for _, s := range sessions {
		ss.sessions[s.ID] = s
	}
}

func (ss *SessionState) persist() {
	if err := os.MkdirAll(ss.stateDir, 0700); err != nil {
		return
	}
	sessions := make([]backend.Session, 0, len(ss.sessions))
	for _, s := range ss.sessions {
		sessions = append(sessions, s)
	}
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	if err := os.WriteFile(ss.filePath, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "[carryon] failed to persist session state: %v\n", err)
	}
}
