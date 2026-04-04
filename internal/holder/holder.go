package holder

import (
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/carryon-dev/cli/internal/pty"
)

const scrollbackMaxBytes = 256 * 1024
const readBufSize = 32 * 1024

// SpawnOpts configures a new holder process.
type SpawnOpts struct {
	SessionID  string
	Shell      string
	Args       []string // passed to pty.Spawn
	Command    string   // original command string for handshake metadata
	Cwd        string
	Cols       uint16
	Rows       uint16
	BaseDir    string
	Env        []string
	Executable string // override for os.Executable() in SpawnProcess (for tests)
}

const clientSendBufSize = 256

// Holder manages a PTY process and its socket for daemon connections.
type Holder struct {
	opts           SpawnOpts
	ptyHandle      pty.Pty
	scrollback     *Scrollback
	sockPath       string
	listener       net.Listener
	mu             sync.Mutex
	clients        map[net.Conn]chan []byte // conn -> send channel
	lastResizeConn net.Conn
	shutdown       bool
	done           chan struct{}
}

// Spawn creates a new Holder: spawns the PTY, starts the socket listener,
// and launches the readPtyLoop and acceptLoop goroutines.
func Spawn(opts SpawnOpts) (*Holder, error) {
	p, err := pty.Spawn(opts.Shell, opts.Args, pty.SpawnOpts{
		Cols: opts.Cols,
		Rows: opts.Rows,
		Cwd:  opts.Cwd,
		Env:  opts.Env,
	})
	if err != nil {
		return nil, err
	}

	sockPath := SocketPath(opts.BaseDir, opts.SessionID)
	ln, err := Listen(sockPath)
	if err != nil {
		p.Close()
		return nil, err
	}

	h := &Holder{
		opts:       opts,
		ptyHandle:  p,
		scrollback: NewScrollback(scrollbackMaxBytes),
		sockPath:   sockPath,
		listener:   ln,
		clients:    make(map[net.Conn]chan []byte),
		done:       make(chan struct{}),
	}

	go h.readPtyLoop()
	go h.acceptLoop()

	return h, nil
}

// Pid returns the PID of the shell process running inside the PTY.
func (h *Holder) Pid() int {
	return h.ptyHandle.Pid()
}

// Done returns a channel that is closed when the holder exits (shell died).
func (h *Holder) Done() <-chan struct{} {
	return h.done
}

// Shutdown closes the PTY, listener, all client connections, and cleans up the socket.
func (h *Holder) Shutdown() {
	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		return
	}
	h.shutdown = true
	clients := make(map[net.Conn]chan []byte, len(h.clients))
	for c, ch := range h.clients {
		clients[c] = ch
	}
	h.clients = make(map[net.Conn]chan []byte)
	h.mu.Unlock()

	for c, ch := range clients {
		close(ch)
		c.Close()
	}
	h.ptyHandle.Close()
	h.listener.Close()
	Cleanup(h.sockPath)
}

// readPtyLoop reads output from the PTY, stores it in scrollback,
// and forwards it to any connected clients as FrameData frames.
// When the PTY returns EOF (shell exited), it sends a FrameExit and cleans up.
//
// On Windows, conpty's Read may not return EOF when the process exits.
// A parallel goroutine calls ptyHandle.Wait() to detect process exit, then
// closes the PTY to unblock Read.
func (h *Holder) readPtyLoop() {
	// Use a sync.Once to safely capture the exit code from whichever
	// call to Wait() completes first (the goroutine or the error path).
	var waitOnce sync.Once
	var exitCode int32

	captureExit := func() {
		waitErr := h.ptyHandle.Wait()
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				exitCode = int32(ee.ExitCode())
			} else {
				exitCode = 1
			}
		}
	}

	// Watch for process exit via Wait(). When the process exits, close the
	// PTY to unblock any blocked Read call. On Unix this is redundant (Read
	// returns EOF naturally) but harmless. On Windows this is essential.
	go func() {
		waitOnce.Do(captureExit)
		// Close the PTY to unblock Read. Use the Holder's Shutdown-safe
		// approach: only close if not already shut down.
		h.mu.Lock()
		alreadyShutdown := h.shutdown
		h.mu.Unlock()
		if !alreadyShutdown {
			h.ptyHandle.Close()
		}
	}()

	buf := make([]byte, readBufSize)
	for {
		n, err := h.ptyHandle.Read(buf)
		if n > 0 {
			data := buf[:n]
			h.scrollback.Write(data)

			frame := EncodeFrame(FrameData, data)
			h.broadcast(frame)
		}
		if err != nil {
			// PTY closed / shell exited. Capture exit code via Once
			// (may already have been captured by the goroutine above).
			waitOnce.Do(captureExit)

			// Send FrameExit to all connected clients.
			exitFrame := EncodeExit(exitCode)
			h.broadcast(exitFrame)

			h.listener.Close()
			Cleanup(h.sockPath)

			// Signal that the holder is done.
			select {
			case <-h.done:
			default:
				close(h.done)
			}
			return
		}
	}
}

// acceptLoop accepts incoming client connections on the socket.
// Multiple connections can be active simultaneously.
// The loop exits when Accept() returns an error, which happens when the
// listener is closed by readPtyLoop or Shutdown.
func (h *Holder) acceptLoop() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}

		// Each client gets a send channel with a writer goroutine, so
		// broadcast() never blocks on a slow connection. If the channel
		// fills up (slow client), frames are dropped rather than stalling
		// other clients.
		ch := make(chan []byte, clientSendBufSize)

		h.mu.Lock()
		h.clients[conn] = ch
		h.mu.Unlock()

		go h.clientWriteLoop(conn, ch)
		h.sendHandshake(conn)
		go h.readClientLoop(conn)
	}
}

// sendHandshake sends the handshake message and scrollback data to a newly connected daemon.
func (h *Holder) sendHandshake(conn net.Conn) {
	scrollData := h.scrollback.Bytes()

	h.mu.Lock()
	hs := Handshake{
		PID:           uint32(h.ptyHandle.Pid()),
		HolderPID:     uint32(os.Getpid()),
		Cols:          h.opts.Cols,
		Rows:          h.opts.Rows,
		ScrollbackLen: uint32(len(scrollData)),
		Cwd:           h.opts.Cwd,
		Command:       h.opts.Command,
	}
	h.mu.Unlock()

	hsBytes, err := hs.Encode()
	if err != nil {
		return
	}

	// Write handshake followed by scrollback data.
	_, _ = conn.Write(hsBytes)
	if len(scrollData) > 0 {
		_, _ = conn.Write(scrollData)
	}
}

// readClientLoop reads frames from a client connection and processes them.
// FrameData is forwarded to the PTY. FrameResize resizes the PTY.
// On any error, the client is removed (client disconnected).
func (h *Holder) readClientLoop(conn net.Conn) {
	buf := make([]byte, 0, readBufSize)
	tmp := make([]byte, readBufSize)

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		// Process all complete frames in the buffer.
		for {
			typ, payload, rest, ferr := DecodeFrame(buf)
			if ferr != nil {
				break // incomplete frame, need more data
			}
			buf = rest

			switch typ {
			case FrameData:
				h.mu.Lock()
				if h.lastResizeConn != nil && conn != h.lastResizeConn {
					resizeReqFrame := EncodeFrame(FrameResizeRequest, nil)
					conn.Write(resizeReqFrame)
					h.lastResizeConn = conn
				}
				h.mu.Unlock()
				if _, werr := h.ptyHandle.Write(payload); werr != nil {
					// PTY write error; it will be caught by readPtyLoop.
				}
			case FrameResize:
				if len(payload) >= 4 {
					cols, rows := DecodeResize(payload)
					_ = h.ptyHandle.Resize(cols, rows)
					h.mu.Lock()
					h.opts.Cols = cols
					h.opts.Rows = rows
					h.lastResizeConn = conn
					h.mu.Unlock()
				}
			case FrameStatusRequest:
				h.mu.Lock()
				sr := StatusResponse{
					PID:         uint32(h.ptyHandle.Pid()),
					HolderPID:   uint32(os.Getpid()),
					Cols:        h.opts.Cols,
					Rows:        h.opts.Rows,
					ClientCount: uint16(len(h.clients)),
				}
				h.mu.Unlock()
				respFrame := EncodeFrame(FrameStatusResponse, sr.Encode())
				_, _ = conn.Write(respFrame)
			}
		}

		if err != nil {
			// Client disconnected. Remove from clients map.
			h.removeClient(conn)
			return
		}
	}
}

// broadcast enqueues a frame to all connected clients' send channels.
// Non-blocking: if a client's channel is full, the frame is dropped for that
// client rather than stalling delivery to others.
func (h *Holder) broadcast(frame []byte) {
	h.mu.Lock()
	// Stack-allocated buffer for the common case (1-2 clients).
	var chBuf [4]chan []byte
	chs := chBuf[:0]
	for _, ch := range h.clients {
		chs = append(chs, ch)
	}
	h.mu.Unlock()

	for _, ch := range chs {
		select {
		case ch <- frame:
		default:
			// slow client, drop frame
		}
	}
}

// clientWriteLoop drains the send channel and writes frames to the connection.
// Exits when the channel is closed (client removed or shutdown).
func (h *Holder) clientWriteLoop(conn net.Conn, ch chan []byte) {
	for frame := range ch {
		if _, err := conn.Write(frame); err != nil {
			h.removeClient(conn)
			return
		}
	}
}

// removeClient removes a client from the clients map, closes its send channel
// (which stops clientWriteLoop), and closes the connection.
func (h *Holder) removeClient(conn net.Conn) {
	h.mu.Lock()
	ch, exists := h.clients[conn]
	if exists {
		delete(h.clients, conn)
	}
	if h.lastResizeConn == conn {
		h.lastResizeConn = nil
	}
	h.mu.Unlock()

	if exists {
		close(ch)
		conn.Close()
	}
}
