package ipc

import (
	"encoding/binary"
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := Frame{
		Type:      backend.FrameTerminalData,
		SessionID: "native-abc123",
		Payload:   []byte("hello world"),
	}

	buf := EncodeFrame(original)

	decoded, rest, err := DecodeFrame(buf)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %d, want %d", decoded.Type, original.Type)
	}
	if decoded.SessionID != original.SessionID {
		t.Errorf("SessionID mismatch: got %q, want %q", decoded.SessionID, original.SessionID)
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("Payload mismatch: got %q, want %q", decoded.Payload, original.Payload)
	}
}

func TestDecodeIncomplete(t *testing.T) {
	frame := Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "test-session",
		Payload:   []byte(`{"jsonrpc":"2.0","method":"ping"}`),
	}

	buf := EncodeFrame(frame)

	// Try decoding only half the bytes.
	half := buf[:len(buf)/2]
	_, _, err := DecodeFrame(half)
	if err != ErrIncomplete {
		t.Fatalf("expected ErrIncomplete, got %v", err)
	}
}

func TestDecodeMultipleFrames(t *testing.T) {
	f1 := Frame{
		Type:      backend.FrameTerminalData,
		SessionID: "s1",
		Payload:   []byte("data-one"),
	}
	f2 := Frame{
		Type:      backend.FrameResize,
		SessionID: "s2",
		Payload:   []byte("data-two"),
	}

	buf := append(EncodeFrame(f1), EncodeFrame(f2)...)

	// Decode first frame.
	d1, rest, err := DecodeFrame(buf)
	if err != nil {
		t.Fatalf("DecodeFrame(1) error: %v", err)
	}
	if d1.SessionID != "s1" {
		t.Errorf("frame 1 SessionID: got %q, want %q", d1.SessionID, "s1")
	}
	if d1.Type != backend.FrameTerminalData {
		t.Errorf("frame 1 Type: got %d, want %d", d1.Type, backend.FrameTerminalData)
	}

	// Decode second frame.
	d2, rest, err := DecodeFrame(rest)
	if err != nil {
		t.Fatalf("DecodeFrame(2) error: %v", err)
	}
	if d2.SessionID != "s2" {
		t.Errorf("frame 2 SessionID: got %q, want %q", d2.SessionID, "s2")
	}
	if d2.Type != backend.FrameResize {
		t.Errorf("frame 2 Type: got %d, want %d", d2.Type, backend.FrameResize)
	}

	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
}

func TestEncodeResizeFrame(t *testing.T) {
	payload := []byte(`{"cols":120,"rows":40}`)
	original := Frame{
		Type:      backend.FrameResize,
		SessionID: "native-resize1",
		Payload:   payload,
	}

	buf := EncodeFrame(original)

	decoded, rest, err := DecodeFrame(buf)
	if err != nil {
		t.Fatalf("DecodeFrame returned error: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %d", len(rest))
	}
	if decoded.Type != backend.FrameResize {
		t.Errorf("Type mismatch: got %d, want %d", decoded.Type, backend.FrameResize)
	}
	if string(decoded.Payload) != string(payload) {
		t.Errorf("Payload mismatch: got %q, want %q", decoded.Payload, payload)
	}
}

func TestFrameDecoderStream(t *testing.T) {
	f1 := Frame{
		Type:      backend.FrameTerminalData,
		SessionID: "stream-1",
		Payload:   []byte("payload-a"),
	}
	f2 := Frame{
		Type:      backend.FrameResize,
		SessionID: "stream-2",
		Payload:   []byte("payload-b"),
	}

	wire := append(EncodeFrame(f1), EncodeFrame(f2)...)

	decoder := NewFrameDecoder()

	var received []Frame
	decoder.OnFrame(func(f Frame) {
		received = append(received, f)
	})

	// Feed in 3-byte chunks to simulate streaming.
	chunkSize := 3
	for i := 0; i < len(wire); i += chunkSize {
		end := i + chunkSize
		if end > len(wire) {
			end = len(wire)
		}
		decoder.Push(wire[i:end])
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(received))
	}

	if received[0].SessionID != "stream-1" {
		t.Errorf("frame 1 SessionID: got %q, want %q", received[0].SessionID, "stream-1")
	}
	if received[0].Type != backend.FrameTerminalData {
		t.Errorf("frame 1 Type: got %d, want %d", received[0].Type, backend.FrameTerminalData)
	}
	if string(received[0].Payload) != "payload-a" {
		t.Errorf("frame 1 Payload: got %q, want %q", received[0].Payload, "payload-a")
	}

	if received[1].SessionID != "stream-2" {
		t.Errorf("frame 2 SessionID: got %q, want %q", received[1].SessionID, "stream-2")
	}
	if received[1].Type != backend.FrameResize {
		t.Errorf("frame 2 Type: got %d, want %d", received[1].Type, backend.FrameResize)
	}
	if string(received[1].Payload) != "payload-b" {
		t.Errorf("frame 2 Payload: got %q, want %q", received[1].Payload, "payload-b")
	}
}

func TestFrameDecoderReportsErrorOnMalformedFrame(t *testing.T) {
	dec := NewFrameDecoder()

	var received []Frame
	dec.OnFrame(func(f Frame) {
		received = append(received, f)
	})

	// Send a frame with sidLen exceeding maxSessionIDLen (4096).
	bad := make([]byte, 5)
	bad[0] = byte(backend.FrameTerminalData)
	binary.BigEndian.PutUint32(bad[1:5], maxSessionIDLen+1)
	dec.Push(bad)

	// After a malformed frame, the decoder should be in an error state.
	// Pushing more data should NOT produce frames - the connection is broken.
	good := EncodeFrame(Frame{
		Type:      backend.FrameTerminalData,
		SessionID: "s1",
		Payload:   []byte("should-not-decode"),
	})
	dec.Push(good)

	if len(received) != 0 {
		t.Errorf("expected 0 frames after malformed frame (connection should be dead), got %d", len(received))
	}

	if !dec.Err() {
		t.Error("expected Err() to return true after malformed frame")
	}
}

func TestFrameDecoderOversizedFrameSetsError(t *testing.T) {
	dec := NewFrameDecoder()

	var received []Frame
	dec.OnFrame(func(f Frame) {
		received = append(received, f)
	})

	// Send a frame with sidLen exceeding maxSessionIDLen (4096).
	// Type byte + 4-byte sidLen = 5 bytes header.
	bad := make([]byte, 5)
	bad[0] = byte(backend.FrameTerminalData)
	binary.BigEndian.PutUint32(bad[1:5], maxSessionIDLen+1) // too large
	dec.Push(bad)

	if !dec.Err() {
		t.Fatal("expected Err() to be true after oversized frame")
	}

	// Further pushes should be no-ops.
	good := EncodeFrame(Frame{
		Type:      backend.FrameTerminalData,
		SessionID: "s1",
		Payload:   []byte("after-reset"),
	})
	dec.Push(good)

	if len(received) != 0 {
		t.Fatalf("expected 0 frames after error, got %d", len(received))
	}
}

func TestFrameResizeRequest(t *testing.T) {
	f := Frame{
		Type:      backend.FrameResizeRequest,
		SessionID: "native-abc123",
		Payload:   nil,
	}
	encoded := EncodeFrame(f)

	decoded, rest, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame error: %v", err)
	}
	if decoded.Type != backend.FrameResizeRequest {
		t.Errorf("Type: got %d, want %d", decoded.Type, backend.FrameResizeRequest)
	}
	if decoded.SessionID != "native-abc123" {
		t.Errorf("SessionID: got %q, want %q", decoded.SessionID, "native-abc123")
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("Payload: got %d bytes, want 0", len(decoded.Payload))
	}
	if len(rest) != 0 {
		t.Errorf("remaining bytes: got %d, want 0", len(rest))
	}
}
