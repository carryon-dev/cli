package holder

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Frame types for daemon<->holder communication.
const (
	FrameData            byte = 0x00
	FrameResize          byte = 0x01
	FrameExit            byte = 0x02
	FrameStatusRequest   byte = 0x03
	FrameStatusResponse  byte = 0x04
	FrameResizeRequest   byte = 0x05
)

// ErrIncomplete is returned when there is not enough data to decode a frame or handshake.
var ErrIncomplete = errors.New("incomplete frame")

// frameHeaderSize is 1 byte type + 4 bytes uint32 length.
const frameHeaderSize = 5

// maxFramePayloadLen caps the payload length to prevent OOM from malformed frames.
const maxFramePayloadLen = 8 * 1024 * 1024 // 8 MiB

// EncodeFrame encodes a frame with the given type and payload.
// Wire format: [1 byte type][4 bytes uint32 length][payload]
func EncodeFrame(typ byte, payload []byte) []byte {
	buf := make([]byte, frameHeaderSize+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// DecodeFrame decodes a single frame from buf.
// Returns the frame type, payload, remaining bytes after the frame, and any error.
// Returns ErrIncomplete if buf does not contain a complete frame.
func DecodeFrame(buf []byte) (typ byte, payload []byte, rest []byte, err error) {
	if len(buf) < frameHeaderSize {
		return 0, nil, buf, ErrIncomplete
	}
	typ = buf[0]
	length := binary.BigEndian.Uint32(buf[1:5])
	if length > maxFramePayloadLen {
		return 0, nil, buf, fmt.Errorf("frame too large: %d bytes (max %d)", length, maxFramePayloadLen)
	}
	total := frameHeaderSize + int(length)
	if len(buf) < total {
		return 0, nil, buf, ErrIncomplete
	}
	payload = buf[frameHeaderSize:total]
	rest = buf[total:]
	return typ, payload, rest, nil
}

// EncodeResize creates a FrameResize frame encoding cols and rows.
func EncodeResize(cols, rows uint16) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], cols)
	binary.BigEndian.PutUint16(payload[2:4], rows)
	return EncodeFrame(FrameResize, payload)
}

// DecodeResize extracts cols and rows from a resize frame payload.
func DecodeResize(payload []byte) (cols, rows uint16) {
	cols = binary.BigEndian.Uint16(payload[0:2])
	rows = binary.BigEndian.Uint16(payload[2:4])
	return cols, rows
}

// EncodeExit creates a FrameExit frame encoding an exit code.
func EncodeExit(code int32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload[0:4], uint32(code))
	return EncodeFrame(FrameExit, payload)
}

// DecodeExit extracts the exit code from an exit frame payload.
func DecodeExit(payload []byte) int32 {
	return int32(binary.BigEndian.Uint32(payload[0:4]))
}

// Handshake is sent by the holder when the daemon connects.
type Handshake struct {
	PID           uint32
	HolderPID     uint32
	Cols          uint16
	Rows          uint16
	ScrollbackLen uint32
	Cwd           string
	Command       string
}

// handshakeFixedSize is the fixed portion of the handshake:
// 4 PID + 4 HolderPID + 2 Cols + 2 Rows + 4 ScrollbackLen + 2 CwdLen + 2 CmdLen = 20
const handshakeFixedSize = 20

// Encode serializes the handshake to bytes.
// Format: [4 PID][4 HolderPID][2 Cols][2 Rows][4 ScrollbackLen][2 CwdLen][Cwd bytes][2 CmdLen][Cmd bytes]
func (h *Handshake) Encode() ([]byte, error) {
	cwdBytes := []byte(h.Cwd)
	cmdBytes := []byte(h.Command)
	if len(cwdBytes) > 0xFFFF {
		return nil, fmt.Errorf("cwd too long for handshake: %d bytes (max 65535)", len(cwdBytes))
	}
	if len(cmdBytes) > 0xFFFF {
		return nil, fmt.Errorf("command too long for handshake: %d bytes (max 65535)", len(cmdBytes))
	}
	buf := make([]byte, handshakeFixedSize+len(cwdBytes)+len(cmdBytes))

	binary.BigEndian.PutUint32(buf[0:4], h.PID)
	binary.BigEndian.PutUint32(buf[4:8], h.HolderPID)
	binary.BigEndian.PutUint16(buf[8:10], h.Cols)
	binary.BigEndian.PutUint16(buf[10:12], h.Rows)
	binary.BigEndian.PutUint32(buf[12:16], h.ScrollbackLen)
	binary.BigEndian.PutUint16(buf[16:18], uint16(len(cwdBytes)))
	copy(buf[18:18+len(cwdBytes)], cwdBytes)
	off := 18 + len(cwdBytes)
	binary.BigEndian.PutUint16(buf[off:off+2], uint16(len(cmdBytes)))
	copy(buf[off+2:], cmdBytes)

	return buf, nil
}

// StatusResponse carries a snapshot of the holder's current state.
// Wire format: [4 PID][4 HolderPID][2 Cols][2 Rows][2 ClientCount] = 14 bytes fixed.
type StatusResponse struct {
	PID         uint32
	HolderPID   uint32
	Cols        uint16
	Rows        uint16
	ClientCount uint16
}

const statusResponseSize = 14

// Encode serializes a StatusResponse to bytes.
func (s *StatusResponse) Encode() []byte {
	buf := make([]byte, statusResponseSize)
	binary.BigEndian.PutUint32(buf[0:4], s.PID)
	binary.BigEndian.PutUint32(buf[4:8], s.HolderPID)
	binary.BigEndian.PutUint16(buf[8:10], s.Cols)
	binary.BigEndian.PutUint16(buf[10:12], s.Rows)
	binary.BigEndian.PutUint16(buf[12:14], s.ClientCount)
	return buf
}

// DecodeStatusResponse deserializes a StatusResponse from a frame payload.
// Returns ErrIncomplete if the payload is too short.
func DecodeStatusResponse(payload []byte) (StatusResponse, error) {
	if len(payload) < statusResponseSize {
		return StatusResponse{}, ErrIncomplete
	}
	return StatusResponse{
		PID:         binary.BigEndian.Uint32(payload[0:4]),
		HolderPID:   binary.BigEndian.Uint32(payload[4:8]),
		Cols:        binary.BigEndian.Uint16(payload[8:10]),
		Rows:        binary.BigEndian.Uint16(payload[10:12]),
		ClientCount: binary.BigEndian.Uint16(payload[12:14]),
	}, nil
}

// DecodeHandshake deserializes a Handshake from buf.
// Returns the decoded Handshake, remaining bytes after the handshake, and any error.
// Returns ErrIncomplete if buf does not contain a complete handshake.
func DecodeHandshake(buf []byte) (Handshake, []byte, error) {
	if len(buf) < handshakeFixedSize {
		return Handshake{}, buf, ErrIncomplete
	}

	h := Handshake{
		PID:           binary.BigEndian.Uint32(buf[0:4]),
		HolderPID:     binary.BigEndian.Uint32(buf[4:8]),
		Cols:          binary.BigEndian.Uint16(buf[8:10]),
		Rows:          binary.BigEndian.Uint16(buf[10:12]),
		ScrollbackLen: binary.BigEndian.Uint32(buf[12:16]),
	}

	cwdLen := int(binary.BigEndian.Uint16(buf[16:18]))
	needed := handshakeFixedSize + cwdLen
	if len(buf) < needed {
		return Handshake{}, buf, ErrIncomplete
	}
	h.Cwd = string(buf[18 : 18+cwdLen])

	off := 18 + cwdLen
	// Need 2 more bytes for CmdLen
	if len(buf) < off+2 {
		return Handshake{}, buf, ErrIncomplete
	}
	cmdLen := int(binary.BigEndian.Uint16(buf[off : off+2]))
	totalNeeded := off + 2 + cmdLen
	if len(buf) < totalNeeded {
		return Handshake{}, buf, ErrIncomplete
	}
	h.Command = string(buf[off+2 : off+2+cmdLen])

	return h, buf[totalNeeded:], nil
}
