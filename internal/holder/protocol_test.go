package holder

import (
	"errors"
	"testing"
)

func TestEncodeDecodeDataFrame(t *testing.T) {
	payload := []byte("hello world")
	frame := EncodeFrame(FrameData, payload)

	typ, got, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameData {
		t.Fatalf("expected type %d, got %d", FrameData, typ)
	}
	if string(got) != string(payload) {
		t.Fatalf("expected payload %q, got %q", payload, got)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
}

func TestEncodeDecodeResizeFrame(t *testing.T) {
	frame := EncodeResize(132, 43)

	typ, payload, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameResize {
		t.Fatalf("expected type %d, got %d", FrameResize, typ)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}

	cols, rows := DecodeResize(payload)
	if cols != 132 || rows != 43 {
		t.Fatalf("expected 132x43, got %dx%d", cols, rows)
	}
}

func TestEncodeDecodeExitFrame(t *testing.T) {
	frame := EncodeExit(42)

	typ, payload, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameExit {
		t.Fatalf("expected type %d, got %d", FrameExit, typ)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}

	code := DecodeExit(payload)
	if code != 42 {
		t.Fatalf("expected exit code 42, got %d", code)
	}
}

func TestEncodeDecodeExitFrameNegative(t *testing.T) {
	frame := EncodeExit(-1)

	typ, payload, _, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameExit {
		t.Fatalf("expected type %d, got %d", FrameExit, typ)
	}

	code := DecodeExit(payload)
	if code != -1 {
		t.Fatalf("expected exit code -1, got %d", code)
	}
}

func TestIncompleteFrame(t *testing.T) {
	frame := EncodeFrame(FrameData, []byte("hello"))

	// Only pass partial header
	_, _, _, err := DecodeFrame(frame[:3])
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected ErrIncomplete for partial header, got %v", err)
	}

	// Pass header but incomplete payload
	_, _, _, err = DecodeFrame(frame[:6])
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected ErrIncomplete for partial payload, got %v", err)
	}
}

func TestMultipleFramesInBuffer(t *testing.T) {
	f1 := EncodeFrame(FrameData, []byte("first"))
	f2 := EncodeResize(80, 24)
	f3 := EncodeExit(0)

	buf := make([]byte, 0, len(f1)+len(f2)+len(f3))
	buf = append(buf, f1...)
	buf = append(buf, f2...)
	buf = append(buf, f3...)

	// Decode first frame
	typ, payload, rest, err := DecodeFrame(buf)
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if typ != FrameData || string(payload) != "first" {
		t.Fatalf("frame 1: unexpected type=%d payload=%q", typ, payload)
	}

	// Decode second frame
	typ, payload, rest, err = DecodeFrame(rest)
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if typ != FrameResize {
		t.Fatalf("frame 2: expected resize, got type=%d", typ)
	}
	cols, rows := DecodeResize(payload)
	if cols != 80 || rows != 24 {
		t.Fatalf("frame 2: expected 80x24, got %dx%d", cols, rows)
	}

	// Decode third frame
	typ, payload, rest, err = DecodeFrame(rest)
	if err != nil {
		t.Fatalf("frame 3: %v", err)
	}
	if typ != FrameExit {
		t.Fatalf("frame 3: expected exit, got type=%d", typ)
	}
	code := DecodeExit(payload)
	if code != 0 {
		t.Fatalf("frame 3: expected exit code 0, got %d", code)
	}

	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	h := Handshake{
		PID:           12345,
		HolderPID:     67890,
		Cols:          120,
		Rows:          40,
		ScrollbackLen: 8192,
		Cwd:           "/home/user/project",
		Command:       "vim main.go",
	}

	encoded, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	decoded, rest, err := DecodeHandshake(encoded)
	if err != nil {
		t.Fatalf("DecodeHandshake returned error: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}

	if decoded.PID != h.PID {
		t.Fatalf("PID: expected %d, got %d", h.PID, decoded.PID)
	}
	if decoded.HolderPID != h.HolderPID {
		t.Fatalf("HolderPID: expected %d, got %d", h.HolderPID, decoded.HolderPID)
	}
	if decoded.Cols != h.Cols {
		t.Fatalf("Cols: expected %d, got %d", h.Cols, decoded.Cols)
	}
	if decoded.Rows != h.Rows {
		t.Fatalf("Rows: expected %d, got %d", h.Rows, decoded.Rows)
	}
	if decoded.ScrollbackLen != h.ScrollbackLen {
		t.Fatalf("ScrollbackLen: expected %d, got %d", h.ScrollbackLen, decoded.ScrollbackLen)
	}
	if decoded.Cwd != h.Cwd {
		t.Fatalf("Cwd: expected %q, got %q", h.Cwd, decoded.Cwd)
	}
	if decoded.Command != h.Command {
		t.Fatalf("Command: expected %q, got %q", h.Command, decoded.Command)
	}
}

func TestHandshakeWithRemainingBytes(t *testing.T) {
	h := Handshake{
		PID:           1,
		HolderPID:     2,
		Cols:          80,
		Rows:          24,
		ScrollbackLen: 0,
		Cwd:           "/tmp",
		Command:       "sh",
	}

	encoded, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	extra := []byte("extra data here")
	buf := append(encoded, extra...)

	decoded, rest, decErr := DecodeHandshake(buf)
	if decErr != nil {
		t.Fatalf("DecodeHandshake returned error: %v", decErr)
	}
	if string(rest) != string(extra) {
		t.Fatalf("expected remaining %q, got %q", extra, rest)
	}
	if decoded.Cwd != "/tmp" || decoded.Command != "sh" {
		t.Fatalf("unexpected decoded values: cwd=%q cmd=%q", decoded.Cwd, decoded.Command)
	}
}

func TestHandshakeIncomplete(t *testing.T) {
	h := Handshake{
		PID:           1,
		HolderPID:     2,
		Cols:          80,
		Rows:          24,
		ScrollbackLen: 0,
		Cwd:           "/home/user",
		Command:       "bash",
	}
	encoded, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}

	// Too short for fixed header
	_, _, err = DecodeHandshake(encoded[:10])
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected ErrIncomplete for short header, got %v", err)
	}

	// Has header but truncated Cwd
	_, _, err = DecodeHandshake(encoded[:19])
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected ErrIncomplete for truncated cwd, got %v", err)
	}

	// Has Cwd but truncated Cmd
	_, _, err = DecodeHandshake(encoded[:len(encoded)-2])
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expected ErrIncomplete for truncated cmd, got %v", err)
	}
}

func TestStatusRequestEncodeDecode(t *testing.T) {
	// FrameStatusRequest has an empty payload.
	frame := EncodeFrame(FrameStatusRequest, nil)

	typ, payload, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameStatusRequest {
		t.Fatalf("expected type 0x%02x, got 0x%02x", FrameStatusRequest, typ)
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(payload))
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
}

func TestStatusResponseEncodeDecode(t *testing.T) {
	sr := StatusResponse{
		PID:         1234,
		HolderPID:   5678,
		Cols:        120,
		Rows:        40,
		ClientCount: 3,
	}

	payload := sr.Encode()
	frame := EncodeFrame(FrameStatusResponse, payload)

	typ, framePayload, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if typ != FrameStatusResponse {
		t.Fatalf("expected type 0x%02x, got 0x%02x", FrameStatusResponse, typ)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}

	decoded, err := DecodeStatusResponse(framePayload)
	if err != nil {
		t.Fatalf("DecodeStatusResponse returned error: %v", err)
	}
	if decoded.PID != sr.PID {
		t.Fatalf("PID: expected %d, got %d", sr.PID, decoded.PID)
	}
	if decoded.HolderPID != sr.HolderPID {
		t.Fatalf("HolderPID: expected %d, got %d", sr.HolderPID, decoded.HolderPID)
	}
	if decoded.Cols != sr.Cols {
		t.Fatalf("Cols: expected %d, got %d", sr.Cols, decoded.Cols)
	}
	if decoded.Rows != sr.Rows {
		t.Fatalf("Rows: expected %d, got %d", sr.Rows, decoded.Rows)
	}
	if decoded.ClientCount != sr.ClientCount {
		t.Fatalf("ClientCount: expected %d, got %d", sr.ClientCount, decoded.ClientCount)
	}
}

func TestHandshakeEmptyStrings(t *testing.T) {
	h := Handshake{
		PID:           100,
		HolderPID:     200,
		Cols:          80,
		Rows:          24,
		ScrollbackLen: 4096,
		Cwd:           "",
		Command:       "",
	}

	encoded, err := h.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	decoded, rest, err := DecodeHandshake(encoded)
	if err != nil {
		t.Fatalf("DecodeHandshake returned error: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
	if decoded.Cwd != "" || decoded.Command != "" {
		t.Fatalf("expected empty strings, got cwd=%q cmd=%q", decoded.Cwd, decoded.Command)
	}
}

func TestFrameResizeRequest(t *testing.T) {
	frame := EncodeFrame(FrameResizeRequest, nil)

	typ, payload, rest, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame error: %v", err)
	}
	if typ != FrameResizeRequest {
		t.Fatalf("expected type %d, got %d", FrameResizeRequest, typ)
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(payload))
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
}
