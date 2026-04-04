package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/pty"
)

// tmuxAttachedPty tracks an attached PTY and its associated stream handle.
type tmuxAttachedPty struct {
	ptyHandle pty.Pty
	id        int
}

// TmuxBackend manages sessions backed by a running tmux server.
type TmuxBackend struct {
	mu               sync.RWMutex
	available        bool
	sessions         map[string]Session
	attachedPtys     map[string]map[int]*tmuxAttachedPty // sessionID -> ptyID -> pty
	createdListeners []func(Session)
	endedListeners   []func(string)
	outputListeners  []func(string, []byte)
	stopCh           chan struct{}
	nextPtyID        int
	lastSync         time.Time
}

// NewTmuxBackend creates a TmuxBackend. If tmux is available on PATH,
// it performs an initial sync and starts a background polling goroutine.
func NewTmuxBackend() *TmuxBackend {
	b := &TmuxBackend{
		sessions:     make(map[string]Session),
		attachedPtys: make(map[string]map[int]*tmuxAttachedPty),
		stopCh:       make(chan struct{}),
	}

	b.available = b.checkAvailability()
	if b.available {
		b.syncSessions()
		go b.pollLoop()
	}

	return b
}

func (b *TmuxBackend) ID() string { return "tmux" }

func (b *TmuxBackend) Available() bool {
	return b.available
}

func (b *TmuxBackend) List() []Session {
	if !b.available {
		return nil
	}
	// Rate-limit sync to at most once per second to avoid excessive subprocess calls.
	b.mu.RLock()
	needsSync := time.Since(b.lastSync) > time.Second
	b.mu.RUnlock()
	if needsSync {
		b.syncSessions()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	sessions := make([]Session, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

func (b *TmuxBackend) Create(opts CreateOpts) (Session, error) {
	if !b.available {
		return Session{}, fmt.Errorf("tmux is not available")
	}

	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("carryon-%d", time.Now().UnixNano())
	}

	args := []string{"new-session", "-d", "-s", name}
	if opts.Cwd != "" {
		args = append(args, "-c", opts.Cwd)
	}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}

	if _, err := b.tmuxCmd(args); err != nil {
		return Session{}, fmt.Errorf("tmux new-session: %w", err)
	}

	b.syncSessions()

	b.mu.RLock()
	var found Session
	var ok bool
	for _, s := range b.sessions {
		if s.Name == name {
			found = s
			ok = true
			break
		}
	}
	b.mu.RUnlock()

	if !ok {
		return Session{}, fmt.Errorf("failed to create tmux session: %s", name)
	}

	// Notify created listeners
	b.mu.RLock()
	listeners := make([]func(Session), len(b.createdListeners))
	copy(listeners, b.createdListeners)
	b.mu.RUnlock()

	for _, listener := range listeners {
		listener(found)
	}

	return found, nil
}

func (b *TmuxBackend) Attach(sessionID string) (StreamHandle, error) {
	b.mu.Lock()
	sess, ok := b.sessions[sessionID]
	if !ok {
		b.mu.Unlock()
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	sess.AttachedClients++
	sess.LastAttached = time.Now().UnixMilli()
	b.sessions[sessionID] = sess

	ptyID := b.nextPtyID
	b.nextPtyID++
	b.mu.Unlock()

	// Spawn a PTY running tmux attach.
	// Ensure TERM is set - CI environments may not have it, causing
	// "terminal does not support clear" errors from tmux.
	env := os.Environ()
	hasTerm := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "TERM=" {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}

	ptyHandle, err := pty.Spawn("tmux", []string{"attach-session", "-t", sess.Name}, pty.SpawnOpts{
		Cols: 80,
		Rows: 24,
		Env:  env,
	})
	if err != nil {
		// Undo the attached client count
		b.mu.Lock()
		if s, exists := b.sessions[sessionID]; exists {
			s.AttachedClients--
			if s.AttachedClients < 0 {
				s.AttachedClients = 0
			}
			b.sessions[sessionID] = s
		}
		b.mu.Unlock()
		return nil, fmt.Errorf("spawn tmux attach: %w", err)
	}

	attached := &tmuxAttachedPty{
		ptyHandle: ptyHandle,
		id:        ptyID,
	}

	b.mu.Lock()
	if b.attachedPtys[sessionID] == nil {
		b.attachedPtys[sessionID] = make(map[int]*tmuxAttachedPty)
	}
	b.attachedPtys[sessionID][ptyID] = attached
	b.mu.Unlock()

	handle := &tmuxStreamHandle{
		backend:   b,
		sessionID: sessionID,
		ptyID:     ptyID,
		ptyHandle: ptyHandle,
	}

	// Start read loop goroutine
	go handle.readLoop()

	return handle, nil
}

func (b *TmuxBackend) Resize(sessionID string, cols, rows uint16) error {
	b.mu.RLock()
	_, ok := b.sessions[sessionID]
	inner := b.attachedPtys[sessionID]
	ptys := make(map[int]*tmuxAttachedPty, len(inner))
	for k, v := range inner {
		ptys[k] = v
	}
	b.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	for _, ap := range ptys {
		_ = ap.ptyHandle.Resize(cols, rows) // ignore errors from dead PTYs
	}
	return nil
}

func (b *TmuxBackend) Rename(sessionID string, name string) error {
	b.mu.RLock()
	sess, ok := b.sessions[sessionID]
	b.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	if _, err := b.tmuxCmd([]string{"rename-session", "-t", sess.Name, name}); err != nil {
		return fmt.Errorf("tmux rename-session: %w", err)
	}

	// Re-key the session in the map since ID is derived from name
	newID := "tmux-" + name
	b.mu.Lock()
	delete(b.sessions, sessionID)
	sess.ID = newID
	sess.Name = name
	b.sessions[newID] = sess

	// Move attached PTYs to new key
	if ptys, exists := b.attachedPtys[sessionID]; exists {
		delete(b.attachedPtys, sessionID)
		b.attachedPtys[newID] = ptys
	}
	b.mu.Unlock()

	return nil
}

func (b *TmuxBackend) GetScrollback(sessionID string) string {
	b.mu.RLock()
	sess, ok := b.sessions[sessionID]
	b.mu.RUnlock()

	if !ok {
		return ""
	}

	output, err := b.tmuxCmd([]string{"capture-pane", "-t", sess.Name, "-p", "-S", "-2000"})
	if err != nil {
		return ""
	}
	return output
}

func (b *TmuxBackend) Kill(sessionID string) error {
	b.mu.RLock()
	sess, ok := b.sessions[sessionID]
	b.mu.RUnlock()

	if !ok {
		return nil
	}

	// Try to kill the tmux session (ignore errors - it may already be dead)
	_, _ = b.tmuxCmd([]string{"kill-session", "-t", sess.Name})

	b.mu.Lock()
	delete(b.sessions, sessionID)
	endedListeners := make([]func(string), len(b.endedListeners))
	copy(endedListeners, b.endedListeners)
	b.mu.Unlock()

	for _, listener := range endedListeners {
		listener(sessionID)
	}

	return nil
}

func (b *TmuxBackend) OnSessionCreated(listener func(Session)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createdListeners = append(b.createdListeners, listener)
}

func (b *TmuxBackend) OnSessionEnded(listener func(string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.endedListeners = append(b.endedListeners, listener)
}

func (b *TmuxBackend) OnOutput(listener func(string, []byte)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outputListeners = append(b.outputListeners, listener)
}

func (b *TmuxBackend) Shutdown() {
	// Signal the poll goroutine to stop
	select {
	case <-b.stopCh:
		// Already closed
	default:
		close(b.stopCh)
	}
	// Do NOT kill tmux sessions - they persist independently
}

// checkAvailability returns true if tmux is on PATH.
func (b *TmuxBackend) checkAvailability() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// pollLoop periodically syncs sessions from the tmux server.
func (b *TmuxBackend) pollLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.syncSessions()
		}
	}
}

// syncSessions queries the tmux server for the current session list and
// updates the internal map. It detects new sessions and ended sessions,
// notifying listeners accordingly.
func (b *TmuxBackend) syncSessions() {
	if !b.available {
		return
	}

	b.mu.Lock()
	b.lastSync = time.Now()
	b.mu.Unlock()

	output, err := b.tmuxCmd([]string{
		"list-sessions",
		"-F",
		"#{session_id}|#{session_name}|#{session_created}|#{session_path}",
	})
	if err != nil {
		// tmux server not running - no sessions
		b.mu.Lock()
		oldSessions := b.sessions
		b.sessions = make(map[string]Session)
		endedListeners := make([]func(string), len(b.endedListeners))
		copy(endedListeners, b.endedListeners)
		b.mu.Unlock()

		for id := range oldSessions {
			for _, listener := range endedListeners {
				listener(id)
			}
		}
		return
	}

	currentIDs := make(map[string]bool)
	var newSessions []Session
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		// parts[0] = tmuxId (e.g. $0), parts[1] = name, parts[2] = created, parts[3] = cwd
		name := parts[1]
		created := parts[2]
		cwd := parts[3]

		id := "tmux-" + name
		currentIDs[id] = true

		createdMs := time.Now().UnixMilli()
		if ts, err := strconv.ParseInt(created, 10, 64); err == nil {
			createdMs = ts * 1000
		}

		b.mu.Lock()
		if _, exists := b.sessions[id]; !exists {
			sess := Session{
				ID:              id,
				Name:            name,
				Backend:         "tmux",
				Created:         createdMs,
				Cwd:             cwd,
				AttachedClients: 0,
			}
			b.sessions[id] = sess
			newSessions = append(newSessions, sess)
		} else {
			// Update fields that may have changed externally
			existing := b.sessions[id]
			existing.Name = name
			existing.Cwd = cwd
			b.sessions[id] = existing
		}
		b.mu.Unlock()
	}

	// Detect ended sessions
	b.mu.Lock()
	var endedIDs []string
	for id := range b.sessions {
		if !currentIDs[id] {
			endedIDs = append(endedIDs, id)
		}
	}
	for _, id := range endedIDs {
		delete(b.sessions, id)
	}
	endedListeners := make([]func(string), len(b.endedListeners))
	copy(endedListeners, b.endedListeners)
	b.mu.Unlock()

	for _, id := range endedIDs {
		for _, listener := range endedListeners {
			listener(id)
		}
	}

	// Notify created listeners for sessions discovered externally (created outside carryOn).
	if len(newSessions) > 0 {
		b.mu.RLock()
		createdListeners := make([]func(Session), len(b.createdListeners))
		copy(createdListeners, b.createdListeners)
		b.mu.RUnlock()

		for _, sess := range newSessions {
			for _, listener := range createdListeners {
				listener(sess)
			}
		}
	}
}

// tmuxCmd runs a tmux command with the given arguments and returns stdout.
// It uses a 5-second timeout context.
func (b *TmuxBackend) tmuxCmd(args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// tmuxStreamHandle implements StreamHandle for a tmux attach PTY.
type tmuxStreamHandle struct {
	backend   *TmuxBackend
	sessionID string
	ptyID     int
	ptyHandle pty.Pty

	mu        sync.Mutex
	listeners map[int]func([]byte) int
	nextID    int
	closed    bool
}

func (h *tmuxStreamHandle) Write(data []byte) error {
	_, err := h.ptyHandle.Write(data)
	return err
}

func (h *tmuxStreamHandle) OnData(listener func([]byte) int) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.listeners == nil {
		h.listeners = make(map[int]func([]byte) int)
	}
	id := h.nextID
	h.nextID++
	h.listeners[id] = listener
	return id
}

func (h *tmuxStreamHandle) OffData(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.listeners, id)
}

func (h *tmuxStreamHandle) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.listeners = nil
	h.mu.Unlock()

	// Remove this PTY from the backend's tracking
	h.backend.mu.Lock()
	if ptys, exists := h.backend.attachedPtys[h.sessionID]; exists {
		delete(ptys, h.ptyID)
	}
	if sess, exists := h.backend.sessions[h.sessionID]; exists {
		sess.AttachedClients--
		if sess.AttachedClients < 0 {
			sess.AttachedClients = 0
		}
		h.backend.sessions[h.sessionID] = sess
	}
	h.backend.mu.Unlock()

	_ = h.ptyHandle.Close()
}

// readLoop reads from the PTY and dispatches data to registered listeners
// and the backend's output listeners.
func (h *tmuxStreamHandle) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := h.ptyHandle.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Dispatch to stream listeners
			h.mu.Lock()
			if h.closed {
				h.mu.Unlock()
				return
			}
			listeners := make(map[int]func([]byte) int, len(h.listeners))
			for k, v := range h.listeners {
				listeners[k] = v
			}
			h.mu.Unlock()

			for _, listener := range listeners {
				listener(data)
			}

			// Notify backend output listeners
			h.backend.mu.RLock()
			outputListeners := make([]func(string, []byte), len(h.backend.outputListeners))
			copy(outputListeners, h.backend.outputListeners)
			h.backend.mu.RUnlock()

			for _, listener := range outputListeners {
				listener(h.sessionID, data)
			}
		}
		if err != nil {
			return
		}
	}
}
