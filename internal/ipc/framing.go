package ipc

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/carryon-dev/cli/internal/backend"
)

// ErrIncomplete is returned when the buffer does not contain a complete frame.
var ErrIncomplete = errors.New("incomplete frame")

// maxSessionIDLen is the maximum allowed session ID length in a frame.
const maxSessionIDLen = 4096

// maxPayloadLen is the maximum allowed payload length in a frame (64 MiB).
const maxPayloadLen = 64 * 1024 * 1024

// Frame represents a single IPC message with a type, session ID, and payload.
//
// Wire format:
//
//	[1 byte: type] [4 bytes: session ID length (big-endian)] [N bytes: session ID]
//	[4 bytes: payload length (big-endian)] [M bytes: payload]
type Frame struct {
	Type      backend.FrameType
	SessionID string
	Payload   []byte
}

// EncodeFrame serializes a Frame into its wire-format byte representation.
func EncodeFrame(f Frame) []byte {
	sidLen := len(f.SessionID)
	payLen := len(f.Payload)

	// 1 (type) + 4 (sid len) + sidLen + 4 (payload len) + payLen
	buf := make([]byte, 1+4+sidLen+4+payLen)

	off := 0

	// Type byte.
	buf[off] = byte(f.Type)
	off++

	// Session ID length (big-endian uint32).
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(sidLen))
	off += 4

	// Session ID bytes.
	copy(buf[off:off+sidLen], f.SessionID)
	off += sidLen

	// Payload length (big-endian uint32).
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(payLen))
	off += 4

	// Payload bytes.
	copy(buf[off:off+payLen], f.Payload)

	return buf
}

// DecodeFrame decodes a single frame from buf. It returns the decoded frame,
// any remaining bytes after the frame, and an error. If buf does not contain
// a complete frame, ErrIncomplete is returned.
func DecodeFrame(buf []byte) (Frame, []byte, error) {
	var f Frame

	// Minimum header: 1 (type) + 4 (sid len) = 5 bytes.
	if len(buf) < 5 {
		return f, buf, ErrIncomplete
	}

	off := 0

	// Type byte.
	f.Type = backend.FrameType(buf[off])
	off++

	// Session ID length.
	sidLen := int(binary.BigEndian.Uint32(buf[off : off+4]))
	off += 4

	if sidLen > maxSessionIDLen {
		return f, nil, fmt.Errorf("session ID too large: %d", sidLen)
	}

	// Need sidLen + 4 (payload len) more bytes at minimum.
	if len(buf) < off+sidLen+4 {
		return f, buf, ErrIncomplete
	}

	// Session ID.
	f.SessionID = string(buf[off : off+sidLen])
	off += sidLen

	// Payload length.
	payLen := int(binary.BigEndian.Uint32(buf[off : off+4]))
	off += 4

	if payLen > maxPayloadLen {
		return f, nil, fmt.Errorf("payload too large: %d", payLen)
	}

	// Need payLen more bytes.
	if len(buf) < off+payLen {
		return f, buf, ErrIncomplete
	}

	// Payload - make a copy so callers can safely retain the frame.
	f.Payload = make([]byte, payLen)
	copy(f.Payload, buf[off:off+payLen])
	off += payLen

	return f, buf[off:], nil
}

// FrameDecoder is a streaming decoder that buffers incoming data and emits
// complete frames via a registered callback.
type FrameDecoder struct {
	buf      []byte
	callback func(Frame)
	err      bool // set on fatal framing error; no further frames will be decoded
}

// NewFrameDecoder creates a new streaming FrameDecoder.
func NewFrameDecoder() *FrameDecoder {
	return &FrameDecoder{}
}

// OnFrame registers a callback that will be invoked for each complete frame.
func (d *FrameDecoder) OnFrame(callback func(Frame)) {
	d.callback = callback
}

// Err returns true if the decoder encountered a fatal framing error.
// Once in error state, no further frames will be decoded. The caller
// should close the connection.
func (d *FrameDecoder) Err() bool {
	return d.err
}

// Push feeds data into the decoder. Any complete frames in the accumulated
// buffer are decoded and emitted via the registered callback.
// After a fatal framing error, Push becomes a no-op.
func (d *FrameDecoder) Push(data []byte) {
	if d.err {
		return
	}
	d.buf = append(d.buf, data...)

	for {
		frame, rest, err := DecodeFrame(d.buf)
		if err == ErrIncomplete {
			return
		}
		if err != nil {
			// Malformed frame (too large, etc.) - mark as broken.
			// The caller must close the connection; no further frames
			// can be decoded because the stream is desynchronized.
			d.buf = nil
			d.err = true
			return
		}
		if d.callback != nil {
			d.callback(frame)
		}
		// Compact buffer when the consumed prefix leaves a large backing array.
		// Only compact when there's remaining data - if rest is empty, the
		// existing backing array will be reused on the next Push.
		if len(rest) > 0 && cap(rest) > 4096 && len(rest) < cap(rest)/4 {
			d.buf = append([]byte(nil), rest...)
		} else {
			d.buf = rest
		}
	}
}
