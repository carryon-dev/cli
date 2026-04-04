package holder

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// HolderClient provides a direct connection to a holder socket,
// bypassing the daemon for terminal I/O.
type HolderClient struct {
	conn            net.Conn
	mu              sync.Mutex
	onData          func([]byte)
	onExit          func(int32)
	onResizeRequest func()
	closed          bool
	gotExit         bool
	done            chan struct{}
}

// ConnectHolder dials the holder socket, reads the handshake and scrollback,
// and returns a ready-to-use HolderClient along with the scrollback data.
func ConnectHolder(sockPath string) (*HolderClient, []byte, error) {
	conn, err := Dial(sockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("dial holder: %w", err)
	}

	// Read handshake with a 5s deadline.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var buf []byte
	tmp := make([]byte, 32*1024)
	var hs Handshake
	var rest []byte

	for {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		hs, rest, err = DecodeHandshake(buf)
		if err == nil {
			break
		}

		if rerr != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("read handshake: %w", rerr)
		}
	}

	// Read scrollback with a 30s deadline.
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	scrollbackLen := int(hs.ScrollbackLen)
	for len(rest) < scrollbackLen {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			rest = append(rest, tmp[:n]...)
		}
		if rerr != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("read scrollback: %w", rerr)
		}
	}

	scrollback := make([]byte, scrollbackLen)
	copy(scrollback, rest[:scrollbackLen])
	overflow := rest[scrollbackLen:]

	// Clear deadline for normal operation.
	conn.SetReadDeadline(time.Time{})

	hc := &HolderClient{
		conn: conn,
		done: make(chan struct{}),
	}

	go hc.readLoop(overflow)

	return hc, scrollback, nil
}

// Write sends terminal input data to the holder.
func (hc *HolderClient) Write(data []byte) error {
	frame := EncodeFrame(FrameData, data)
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.closed {
		return fmt.Errorf("client closed")
	}
	_, err := hc.conn.Write(frame)
	return err
}

// Resize sends a resize frame to the holder.
func (hc *HolderClient) Resize(cols, rows uint16) error {
	frame := EncodeResize(cols, rows)
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.closed {
		return fmt.Errorf("client closed")
	}
	_, err := hc.conn.Write(frame)
	return err
}

// Close shuts down the holder client connection.
func (hc *HolderClient) Close() {
	hc.mu.Lock()
	if hc.closed {
		hc.mu.Unlock()
		return
	}
	hc.closed = true
	hc.mu.Unlock()
	hc.conn.Close()
}

// Done returns a channel that closes when the read loop exits.
func (hc *HolderClient) Done() <-chan struct{} {
	return hc.done
}

// OnData registers a callback for terminal output data from the holder.
func (hc *HolderClient) OnData(fn func([]byte)) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.onData = fn
}

// OnExit registers a callback for when the holder reports the shell exited.
func (hc *HolderClient) OnExit(fn func(int32)) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.onExit = fn
}

// OnResizeRequest registers a callback for when the holder requests
// the client's current terminal dimensions.
func (hc *HolderClient) OnResizeRequest(fn func()) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.onResizeRequest = fn
}

// readLoop reads frames from the holder connection and dispatches them
// to the registered callbacks. It processes any overflow bytes from the
// handshake read first.
func (hc *HolderClient) readLoop(overflow []byte) {
	defer close(hc.done)
	defer func() {
		// If the connection broke without a proper FrameExit, signal
		// unexpected disconnect with exit code -1.
		hc.mu.Lock()
		gotExit := hc.gotExit
		fn := hc.onExit
		hc.mu.Unlock()
		if !gotExit && fn != nil {
			fn(-1)
		}
	}()

	var buf []byte
	if len(overflow) > 0 {
		buf = make([]byte, len(overflow))
		copy(buf, overflow)
	}

	// Process any complete frames already in overflow.
	buf = hc.processFrames(buf)

	tmp := make([]byte, 32*1024)
	for {
		n, err := hc.conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			buf = hc.processFrames(buf)
		}
		if err != nil {
			return
		}
	}
}

// processFrames decodes and dispatches all complete frames in buf,
// returning any remaining incomplete data.
func (hc *HolderClient) processFrames(buf []byte) []byte {
	for {
		typ, payload, rest, err := DecodeFrame(buf)
		if err != nil {
			return buf
		}
		buf = rest

		switch typ {
		case FrameData:
			hc.mu.Lock()
			fn := hc.onData
			hc.mu.Unlock()
			if fn != nil {
				fn(payload)
			}
		case FrameResizeRequest:
			hc.mu.Lock()
			fn := hc.onResizeRequest
			hc.mu.Unlock()
			if fn != nil {
				fn()
			}
		case FrameExit:
			var code int32
			if len(payload) >= 4 {
				code = int32(binary.BigEndian.Uint32(payload[:4]))
			}
			hc.mu.Lock()
			hc.gotExit = true
			fn := hc.onExit
			hc.mu.Unlock()
			if fn != nil {
				fn(code)
			}
		}
	}
}
