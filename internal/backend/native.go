package backend

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/carryon-dev/cli/internal/holder"
)

const (
	subscriberChanSize = 256 // buffered channels prevent slow clients from blocking
)

// nativeProcess represents a single holder-backed session.
type nativeProcess struct {
	mu         sync.RWMutex
	conn       net.Conn           // connection to holder socket
	session    Session
	scrollback *holder.Scrollback // local copy from holder
	subs       map[int]chan []byte
	nextSubID  int
	holderPID  int            // PID of the holder process
	holderRef  *holder.Holder // non-nil if we spawned it (nil for recovered sessions)
	closed     bool
}

// NativeBackend manages sessions using local PTY processes via holders.
type NativeBackend struct {
	mu               sync.RWMutex
	baseDir          string
	detached         bool   // when true, holders run as separate processes that survive daemon restart
	executable       string // override for os.Executable in SpawnProcess (for tests)
	processes        map[string]*nativeProcess
	createdListeners []func(Session)
	endedListeners   []func(string)
	outputListeners         []func(string, []byte)
	resizeRequestListeners []func(string) // sessionID
}

// NewNativeBackend creates a new NativeBackend. When detached is true,
// holders are spawned as separate processes via re-exec and survive daemon restart.
// When false, holders run in-process (used for tests).
func NewNativeBackend(baseDir string, detached bool) *NativeBackend {
	return &NativeBackend{
		baseDir:   baseDir,
		detached:  detached,
		processes: make(map[string]*nativeProcess),
	}
}

// SetExecutable overrides the binary path used by SpawnProcess (for tests).
func (b *NativeBackend) SetExecutable(path string) { b.executable = path }

func (b *NativeBackend) ID() string     { return "native" }
func (b *NativeBackend) Available() bool { return true }

func (b *NativeBackend) List() []Session {
	b.mu.RLock()
	defer b.mu.RUnlock()
	sessions := make([]Session, 0, len(b.processes))
	for _, p := range b.processes {
		p.mu.RLock()
		s := p.session // copy
		p.mu.RUnlock()
		sessions = append(sessions, s)
	}
	return sessions
}

func (b *NativeBackend) Create(opts CreateOpts) (Session, error) {
	idBytes := make([]byte, 6)
	if _, err := rand.Read(idBytes); err != nil {
		return Session{}, fmt.Errorf("generate session ID: %w", err)
	}
	id := "native-" + hex.EncodeToString(idBytes)

	shell := opts.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "powershell.exe"
		} else {
			shell = "/bin/sh"
		}
	}

	var args []string
	if opts.Command != "" {
		args = []string{"-c", opts.Command}
	}

	name := opts.Name
	if name == "" {
		name = "session-" + id[7:13]
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	spawnOpts := holder.SpawnOpts{
		SessionID:  id,
		Shell:      shell,
		Args:       args,
		Command:    opts.Command,
		Cwd:        cwd,
		Cols:       80,
		Rows:       24,
		BaseDir:    b.baseDir,
		Env:        os.Environ(),
		Executable: b.executable,
	}

	var holderRef *holder.Holder
	var holderPID int

	if b.detached {
		// Spawn as separate process - survives daemon restart.
		sockPath, pid, err := holder.SpawnProcess(spawnOpts)
		if err != nil {
			return Session{}, fmt.Errorf("spawn holder process: %w", err)
		}
		holderPID = pid
		_ = sockPath
	} else {
		// Spawn in-process - for tests.
		h, err := holder.Spawn(spawnOpts)
		if err != nil {
			return Session{}, fmt.Errorf("spawn holder: %w", err)
		}
		holderRef = h
		holderPID = h.Pid()
	}

	// Connect to the holder socket.
	sockPath := holder.SocketPath(b.baseDir, id)
	conn, hs, scrollData, overflow, err := b.connectToHolder(sockPath)
	if err != nil {
		if holderRef != nil {
			holderRef.Shutdown()
		}
		return Session{}, fmt.Errorf("connect to holder: %w", err)
	}

	sess := Session{
		ID:              id,
		Name:            name,
		Backend:         "native",
		PID:             int(hs.PID),
		Created:         time.Now().UnixMilli(),
		Cwd:             cwd,
		Command:         opts.Command,
		AttachedClients: 0,
	}

	scrollback := holder.NewScrollback(256 * 1024)
	if len(scrollData) > 0 {
		scrollback.Write(scrollData)
	}

	proc := &nativeProcess{
		conn:       conn,
		session:    sess,
		scrollback: scrollback,
		subs:       make(map[int]chan []byte),
		holderPID:  holderPID,
		holderRef:  holderRef,
	}

	b.mu.Lock()
	b.processes[id] = proc
	createdListeners := make([]func(Session), len(b.createdListeners))
	copy(createdListeners, b.createdListeners)
	b.mu.Unlock()

	// Start the read loop goroutine with any overflow bytes from the
	// handshake read that may contain early frame data.
	go b.readLoop(id, proc, overflow)

	// Watch for holder exit (shell died). Close the connection to unblock
	// readLoop's conn.Read, letting it process any remaining buffered data
	// before cleaning up. Do not call handleSessionEnded directly - that
	// races with readLoop and can delete the session before buffered
	// output (like echo) is written to scrollback.
	if holderRef != nil {
		go func() {
			<-holderRef.Done()
			proc.mu.Lock()
			if proc.conn != nil {
				proc.conn.Close()
				proc.conn = nil
			}
			proc.mu.Unlock()
		}()
	}

	// Notify created listeners
	for _, listener := range createdListeners {
		listener(sess)
	}

	return sess, nil
}

// connectToHolder connects to a holder socket, reads the handshake and
// scrollback data, and returns the connection ready for frame I/O.
// The returned overflow slice contains any extra bytes read beyond the
// scrollback (e.g. frame data that arrived in the same read). Callers
// must feed overflow into readLoop to avoid data loss.
func (b *NativeBackend) connectToHolder(sockPath string) (net.Conn, holder.Handshake, []byte, []byte, error) {
	conn, err := holder.Dial(sockPath)
	if err != nil {
		return nil, holder.Handshake{}, nil, nil, fmt.Errorf("dial holder: %w", err)
	}

	// Set a read deadline for the handshake.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Read handshake + scrollback data.
	var buf []byte
	tmp := make([]byte, 32*1024)
	var hs holder.Handshake
	var scrollData []byte
	handshakeDone := false

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		if !handshakeDone {
			h, rest, herr := holder.DecodeHandshake(buf)
			if herr == nil {
				hs = h
				buf = rest
				handshakeDone = true

				// Clear handshake deadline, then set a generous scrollback
				// deadline - scrollback may be large but a stalled holder
				// should not block forever.
				conn.SetReadDeadline(time.Now().Add(30 * time.Second))

				// Now check if we have all scrollback data.
				if uint32(len(buf)) >= hs.ScrollbackLen {
					scrollData = make([]byte, hs.ScrollbackLen)
					copy(scrollData, buf[:hs.ScrollbackLen])
					overflow := append([]byte(nil), buf[hs.ScrollbackLen:]...)
					conn.SetReadDeadline(time.Time{})
					return conn, hs, scrollData, overflow, nil
				}
			}
		} else {
			// Waiting for remaining scrollback data.
			if uint32(len(buf)) >= hs.ScrollbackLen {
				scrollData = make([]byte, hs.ScrollbackLen)
				copy(scrollData, buf[:hs.ScrollbackLen])
				overflow := append([]byte(nil), buf[hs.ScrollbackLen:]...)
				conn.SetReadDeadline(time.Time{})
				return conn, hs, scrollData, overflow, nil
			}
		}

		if err != nil {
			conn.Close()
			return nil, holder.Handshake{}, nil, nil, fmt.Errorf("read handshake: %w", err)
		}
	}
}

// processFrames decodes and dispatches all complete frames in buf. Returns the
// remaining (unconsumed) bytes and true if a FrameExit was seen.
func (b *NativeBackend) processFrames(id string, proc *nativeProcess, buf []byte) ([]byte, bool) {
	for {
		typ, payload, rest, ferr := holder.DecodeFrame(buf)
		if ferr != nil {
			break // incomplete frame, need more data
		}
		buf = rest

		switch typ {
		case holder.FrameData:
			data := make([]byte, len(payload))
			copy(data, payload)

			// Write to scrollback and snapshot subscribers under one lock.
			proc.mu.Lock()
			proc.scrollback.Write(data)
			// Snapshot into a stack-allocated slice for the common case
			// (1-2 subscribers) to avoid a map allocation per frame.
			var subsBuf [4]chan []byte
			subs := subsBuf[:0]
			for _, v := range proc.subs {
				subs = append(subs, v)
			}
			proc.mu.Unlock()

			// Fan out to subscriber channels (non-blocking)
			for _, ch := range subs {
				select {
				case ch <- data:
				default:
					// slow client, drop data
				}
			}

			// Notify output listeners (skip allocation when none registered)
			b.mu.RLock()
			n := len(b.outputListeners)
			var outputListeners []func(string, []byte)
			if n > 0 {
				outputListeners = make([]func(string, []byte), n)
				copy(outputListeners, b.outputListeners)
			}
			b.mu.RUnlock()
			for _, listener := range outputListeners {
				listener(id, data)
			}

		case holder.FrameResizeRequest:
			b.mu.RLock()
			n := len(b.resizeRequestListeners)
			var listeners []func(string)
			if n > 0 {
				listeners = make([]func(string), n)
				copy(listeners, b.resizeRequestListeners)
			}
			b.mu.RUnlock()
			for _, listener := range listeners {
				listener(id)
			}

		case holder.FrameExit:
			return buf, true
		}
	}
	return buf, false
}

// readLoop reads holder frames from the connection and fans out data to
// scrollback, subscribers, and output listeners. On error or FrameExit,
// it triggers session cleanup.
func (b *NativeBackend) readLoop(id string, proc *nativeProcess, initial []byte) {
	buf := append([]byte(nil), initial...)
	tmp := make([]byte, 32*1024)

	for {
		var exited bool
		buf, exited = b.processFrames(id, proc, buf)
		if exited {
			b.handleSessionEnded(id)
			return
		}

		proc.mu.RLock()
		closed := proc.closed
		conn := proc.conn
		proc.mu.RUnlock()
		if closed || conn == nil {
			return
		}

		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		if err != nil {
			// Process any data that arrived with the error before cleaning up.
			buf, exited = b.processFrames(id, proc, buf)
			if exited {
				b.handleSessionEnded(id)
				return
			}

			// Check if this was a clean shutdown (not a holder crash).
			proc.mu.RLock()
			wasClosed := proc.closed
			proc.mu.RUnlock()
			if !wasClosed {
				b.handleSessionEnded(id)
			}
			return
		}
	}
}

// handleSessionEnded cleans up a session when its holder exits or the
// connection is lost. It is safe to call multiple times for the same ID.
func (b *NativeBackend) handleSessionEnded(id string) {
	b.mu.Lock()
	proc, ok := b.processes[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.processes, id)
	endedListeners := make([]func(string), len(b.endedListeners))
	copy(endedListeners, b.endedListeners)
	b.mu.Unlock()

	// Mark the process as closed and close the connection.
	proc.mu.Lock()
	alreadyClosed := proc.closed
	if !alreadyClosed {
		proc.closed = true
		if proc.conn != nil {
			proc.conn.Close()
			proc.conn = nil
		}
	}

	// Always close any remaining subscriber channels so that forward()
	// goroutines can exit. This handles the race where proc.closed was
	// already set (e.g. by Kill) before handleSessionEnded ran, leaving
	// subscriber channels open.
	for subID, ch := range proc.subs {
		close(ch)
		delete(proc.subs, subID)
	}
	proc.mu.Unlock()

	if alreadyClosed {
		// Already cleaned up - just fire listeners.
		for _, listener := range endedListeners {
			listener(id)
		}
		return
	}

	for _, listener := range endedListeners {
		listener(id)
	}
}

func (b *NativeBackend) Attach(sessionID string) (StreamHandle, error) {
	b.mu.RLock()
	proc, ok := b.processes[sessionID]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	proc.mu.Lock()
	proc.session.AttachedClients++
	proc.session.LastAttached = time.Now().UnixMilli()
	subID := proc.nextSubID
	proc.nextSubID++
	ch := make(chan []byte, subscriberChanSize)
	proc.subs[subID] = ch
	proc.mu.Unlock()

	return &nativeStreamHandle{
		proc:  proc,
		subID: subID,
		ch:    ch,
	}, nil
}

func (b *NativeBackend) Resize(sessionID string, cols, rows uint16) error {
	b.mu.RLock()
	proc, ok := b.processes[sessionID]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	frame := holder.EncodeResize(cols, rows)
	proc.mu.RLock()
	conn := proc.conn
	proc.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("session not connected: %s", sessionID)
	}
	_, err := conn.Write(frame)
	return err
}

func (b *NativeBackend) Rename(sessionID string, name string) error {
	b.mu.RLock()
	proc, ok := b.processes[sessionID]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	proc.mu.Lock()
	proc.session.Name = name
	proc.mu.Unlock()
	return nil
}

func (b *NativeBackend) GetScrollback(sessionID string) string {
	b.mu.RLock()
	proc, ok := b.processes[sessionID]
	b.mu.RUnlock()
	if !ok {
		return ""
	}
	proc.mu.RLock()
	defer proc.mu.RUnlock()
	return string(proc.scrollback.Bytes())
}

func (b *NativeBackend) Kill(sessionID string) error {
	b.mu.RLock()
	proc, ok := b.processes[sessionID]
	b.mu.RUnlock()
	if !ok {
		return nil
	}

	proc.mu.RLock()
	h := proc.holderRef
	holderPID := proc.holderPID
	proc.mu.RUnlock()

	if h != nil {
		// We spawned this holder in-process - use the Shutdown method.
		h.Shutdown()
	} else if holderPID > 0 {
		// Recovered session - signal the holder process.
		p, err := os.FindProcess(holderPID)
		if err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}

	// Close and nil the connection to trigger readLoop exit and prevent
	// concurrent Resize writes to a closed connection.
	proc.mu.Lock()
	if proc.conn != nil {
		proc.conn.Close()
		proc.conn = nil
	}
	proc.mu.Unlock()

	return nil
}

func (b *NativeBackend) OnSessionCreated(listener func(Session)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createdListeners = append(b.createdListeners, listener)
}

func (b *NativeBackend) OnSessionEnded(listener func(string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.endedListeners = append(b.endedListeners, listener)
}

func (b *NativeBackend) OnOutput(listener func(string, []byte)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outputListeners = append(b.outputListeners, listener)
}

// OnResizeRequest registers a listener called when the holder requests
// a resize from a daemon-connected client (another client resized the PTY directly).
func (b *NativeBackend) OnResizeRequest(listener func(sessionID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resizeRequestListeners = append(b.resizeRequestListeners, listener)
}

// Shutdown closes all connections. For in-process holders (spawned directly
// via holder.Spawn), it also shuts them down. In production, holders are
// separate processes and survive daemon restart.
func (b *NativeBackend) Shutdown() {
	b.mu.Lock()
	procs := make([]*nativeProcess, 0, len(b.processes))
	for _, p := range b.processes {
		procs = append(procs, p)
	}
	b.mu.Unlock()

	for _, p := range procs {
		p.mu.Lock()
		if p.conn != nil {
			p.conn.Close()
			p.conn = nil
		}
		// If we spawned the holder in-process, shut it down too.
		if p.holderRef != nil {
			p.holderRef.Shutdown()
		}
		p.closed = true
		// Close all subscriber channels so forward() goroutines can exit
		// their range loop instead of blocking forever.
		for subID, ch := range p.subs {
			close(ch)
			delete(p.subs, subID)
		}
		p.mu.Unlock()
	}
}

// Recover reconnects to an existing holder socket for a previously known
// session and registers it back into the process map.
func (b *NativeBackend) Recover(sess Session) error {
	sockPath := holder.SocketPath(b.baseDir, sess.ID)
	conn, hs, scrollData, overflow, err := b.connectToHolder(sockPath)
	if err != nil {
		return fmt.Errorf("recover session %s: %w", sess.ID, err)
	}

	scrollback := holder.NewScrollback(256 * 1024)
	if len(scrollData) > 0 {
		scrollback.Write(scrollData)
	}

	// Update PID from handshake in case it changed.
	sess.PID = int(hs.PID)

	proc := &nativeProcess{
		conn:       conn,
		session:    sess,
		scrollback: scrollback,
		subs:       make(map[int]chan []byte),
		holderPID:  int(hs.HolderPID),
	}

	b.mu.Lock()
	b.processes[sess.ID] = proc
	b.mu.Unlock()

	go b.readLoop(sess.ID, proc, overflow)

	return nil
}

// nativeStreamHandle implements StreamHandle by forwarding data from
// a subscriber channel to registered listeners.
type nativeStreamHandle struct {
	proc  *nativeProcess
	subID int
	ch    chan []byte

	mu        sync.Mutex
	listeners map[int]func([]byte) int
	nextID    int
	started   bool
	closed    bool
}

func (h *nativeStreamHandle) Write(data []byte) error {
	frame := holder.EncodeFrame(holder.FrameData, data)
	h.proc.mu.RLock()
	conn := h.proc.conn
	h.proc.mu.RUnlock()
	if conn == nil {
		return io.ErrClosedPipe
	}
	_, err := conn.Write(frame)
	return err
}

func (h *nativeStreamHandle) OnData(listener func([]byte) int) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return -1
	}

	if h.listeners == nil {
		h.listeners = make(map[int]func([]byte) int)
	}
	id := h.nextID
	h.nextID++
	h.listeners[id] = listener

	// Start the forward goroutine on first OnData call
	if !h.started {
		h.started = true
		go h.forward()
	}

	return id
}

func (h *nativeStreamHandle) OffData(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.listeners, id)
}

func (h *nativeStreamHandle) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.listeners = nil
	h.mu.Unlock()

	// Remove subscriber from the process and close the channel so that
	// the forward() goroutine can exit its range loop.
	h.proc.mu.Lock()
	if _, ok := h.proc.subs[h.subID]; ok {
		delete(h.proc.subs, h.subID)
		close(h.ch)
	}
	h.proc.session.AttachedClients--
	if h.proc.session.AttachedClients < 0 {
		h.proc.session.AttachedClients = 0
	}
	h.proc.mu.Unlock()
}

// forward reads from the subscriber channel and dispatches to listeners.
func (h *nativeStreamHandle) forward() {
	for data := range h.ch {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return
		}
		// Copy listener map for safe iteration
		listeners := make(map[int]func([]byte) int, len(h.listeners))
		for k, v := range h.listeners {
			listeners[k] = v
		}
		h.mu.Unlock()

		for _, listener := range listeners {
			listener(data)
		}
	}
}
