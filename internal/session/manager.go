package session

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/carryon-dev/cli/internal/backend"
)

// stripTerminalQueries removes escape sequences that trigger terminal
// responses when replayed. These are one-time queries/responses between
// the shell and terminal that produce garbage when replayed on reconnect.
var terminalQueryRe = regexp.MustCompile(
	`\x1b\[\??\d*[cn]` + // DA primary (Device Attributes query)
		`|\x1b\[>\d*[cn]` + // DA secondary query
		`|\x1b\[=\d*[cn]` + // DA tertiary query
		`|\x1b\[\?\d+(?:;\d+)*c` + // DA response (e.g. ESC[?64;1;2c)
		`|\x1b\[>\d+(?:;\d+)*c` + // Secondary DA response
		`|\x1b\[\d*;?\d*R` + // Cursor Position Report response
		`|\x1b\[\d*n` + // DSR (Device Status Report)
		`|\x1b\[5n` + // DSR status query
		`|\x1b\[6n` + // DSR cursor position query
		`|\x1b\](?:10|11|4|52)\;[^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC color/clipboard queries
		`|\x1bP[^\\]*\x1b\\` + // DCS sequences (device control strings)
		`|\x1b\]l[^\x07]*\x07`, // OSC title query response
)

// Visible text artifacts from terminal identification responses that leak
// into scrollback when shells don't consume DA responses properly.
var terminalArtifactRe = regexp.MustCompile(
	`>\|xterm\.js\([^)]*\)` + // xterm.js version strings like >|xterm.js(6.1.0-beta.191)
		`|\d+;\d+;\d+c`, // numeric DA response leaked as text like 1;2c
)

func sanitizeScrollback(s string) string {
	s = terminalQueryRe.ReplaceAllString(s, "")
	s = terminalArtifactRe.ReplaceAllString(s, "")
	return s
}

// Manager orchestrates sessions across all registered backends.
type Manager struct {
	registry       *backend.Registry
	defaultBackend string

	mu               sync.RWMutex
	createdListeners  []func(backend.Session)
	endedListeners    []func(string)
	renamedListeners  []func(string, string)
	attachedListeners []func(string)
}

// NewManager creates a new session Manager wired to the given registry.
// It registers event listeners on all currently registered backends.
func NewManager(registry *backend.Registry, defaultBackend string) *Manager {
	m := &Manager{
		registry:       registry,
		defaultBackend: defaultBackend,
	}

	// Wire up backend events to the manager
	for _, b := range registry.GetAll() {
		b.OnSessionCreated(func(sess backend.Session) {
			m.mu.RLock()
			listeners := make([]func(backend.Session), len(m.createdListeners))
			copy(listeners, m.createdListeners)
			m.mu.RUnlock()
			for _, listener := range listeners {
				listener(sess)
			}
		})
		b.OnSessionEnded(func(id string) {
			m.mu.RLock()
			listeners := make([]func(string), len(m.endedListeners))
			copy(listeners, m.endedListeners)
			m.mu.RUnlock()
			for _, listener := range listeners {
				listener(id)
			}
		})
	}

	return m
}

// List returns all sessions from all available backends, sorted alphabetically by name.
func (m *Manager) List() []backend.Session {
	var sessions []backend.Session
	for _, b := range m.registry.GetAvailable() {
		sessions = append(sessions, b.List()...)
	}
	sort.Slice(sessions, func(i, j int) bool {
		cmp := strings.Compare(strings.ToLower(sessions[i].Name), strings.ToLower(sessions[j].Name))
		if cmp != 0 {
			return cmp < 0
		}
		return sessions[i].ID < sessions[j].ID
	})
	return sessions
}

// Get returns a specific session by ID, or nil if not found.
// Uses the session ID prefix to route directly to the correct backend
// instead of scanning all sessions.
func (m *Manager) Get(id string) *backend.Session {
	b, err := m.findBackend(id)
	if err != nil {
		return nil
	}
	for _, s := range b.List() {
		if s.ID == id {
			return &s
		}
	}
	return nil
}

// Create starts a new session using the appropriate backend.
func (m *Manager) Create(opts backend.CreateOpts) (backend.Session, error) {
	requestedID := opts.Backend
	if requestedID == "" {
		requestedID = m.defaultBackend
	}
	if requestedID != "" {
		explicit := m.registry.Get(requestedID)
		if explicit == nil {
			return backend.Session{}, fmt.Errorf("backend not available: %s", requestedID)
		}
	}
	b := m.registry.GetDefault(requestedID)
	if b == nil {
		fallback := requestedID
		if fallback == "" {
			fallback = "native"
		}
		return backend.Session{}, fmt.Errorf("backend not available: %s", fallback)
	}
	return b.Create(opts)
}

// Attach returns a StreamHandle for the given session.
func (m *Manager) Attach(sessionID string) (backend.StreamHandle, error) {
	b, err := m.findBackend(sessionID)
	if err != nil {
		return nil, err
	}
	handle, err := b.Attach(sessionID)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	listeners := make([]func(string), len(m.attachedListeners))
	copy(listeners, m.attachedListeners)
	m.mu.RUnlock()
	for _, listener := range listeners {
		listener(sessionID)
	}

	return handle, nil
}

// Resize changes the terminal dimensions of the given session.
func (m *Manager) Resize(sessionID string, cols, rows uint16) error {
	b, err := m.findBackend(sessionID)
	if err != nil {
		return err
	}
	return b.Resize(sessionID, cols, rows)
}

// Rename changes the display name of the given session.
func (m *Manager) Rename(sessionID string, name string) error {
	b, err := m.findBackend(sessionID)
	if err != nil {
		return err
	}
	if err := b.Rename(sessionID, name); err != nil {
		return err
	}

	m.mu.RLock()
	listeners := make([]func(string, string), len(m.renamedListeners))
	copy(listeners, m.renamedListeners)
	m.mu.RUnlock()
	for _, listener := range listeners {
		listener(sessionID, name)
	}

	return nil
}

// GetScrollback returns the scrollback buffer for the given session,
// with terminal query sequences stripped so they don't trigger responses on replay.
func (m *Manager) GetScrollback(sessionID string) string {
	b, err := m.findBackend(sessionID)
	if err != nil {
		return ""
	}
	return sanitizeScrollback(b.GetScrollback(sessionID))
}

// Kill terminates the given session.
func (m *Manager) Kill(sessionID string) error {
	b, err := m.findBackend(sessionID)
	if err != nil {
		return err
	}
	return b.Kill(sessionID)
}

// OnSessionCreated registers a listener for session creation events.
func (m *Manager) OnSessionCreated(listener func(backend.Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdListeners = append(m.createdListeners, listener)
}

// OnSessionEnded registers a listener for session end events.
func (m *Manager) OnSessionEnded(listener func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endedListeners = append(m.endedListeners, listener)
}

// OnSessionRenamed registers a listener for session rename events.
func (m *Manager) OnSessionRenamed(listener func(string, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renamedListeners = append(m.renamedListeners, listener)
}

// OnSessionAttached registers a listener for session attach events.
func (m *Manager) OnSessionAttached(listener func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachedListeners = append(m.attachedListeners, listener)
}

// Shutdown terminates all backends.
func (m *Manager) Shutdown() {
	m.registry.ShutdownAll()
}

// findBackend extracts the backend ID from the session ID format "backend-xxx"
// and looks it up in the registry.
func (m *Manager) findBackend(sessionID string) (backend.Backend, error) {
	parts := strings.SplitN(sessionID, "-", 2)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no backend found for session: %s", sessionID)
	}
	backendID := parts[0]
	b := m.registry.Get(backendID)
	if b == nil {
		return nil, fmt.Errorf("no backend found for session: %s", sessionID)
	}
	return b, nil
}
