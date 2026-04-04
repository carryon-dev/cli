package remote

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/crypto"
)

// RelayBridge manages an E2E encrypted connection through a QUIC relay node.
type RelayBridge struct {
	conn    *quic.Conn
	stream  *quic.Stream
	cipher  *crypto.StreamCipher
	writeMu sync.Mutex
}

// NewRelayBridge dials the relay at relayAddr via QUIC, sends the pairing token
// on stream 0, derives a StreamCipher from myPriv + theirPub, and returns a
// bridge ready for encrypted I/O.
// Set skipTLSVerify to true for self-hosted relays with self-signed certificates.
// This should be controlled by the local user's config, never by the signaling server.
func NewRelayBridge(ctx context.Context, relayAddr, pairingToken string, myPriv, theirPub []byte, initiator bool, skipTLSVerify bool) (*RelayBridge, error) {
	conn, err := quic.DialAddr(ctx, relayAddr, &tls.Config{
		InsecureSkipVerify: skipTLSVerify, //nolint:gosec - user-controlled via config
		NextProtos:         []string{"carryon"},
	}, &quic.Config{})
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		conn.CloseWithError(1, "open stream failed")
		return nil, fmt.Errorf("open stream: %w", err)
	}

	// Send length-prefixed pairing token.
	tokenBytes := []byte(pairingToken)
	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(tokenBytes)))
	if _, err := stream.Write(lenBuf); err != nil {
		conn.CloseWithError(1, "write token failed")
		return nil, fmt.Errorf("write token length: %w", err)
	}
	if _, err := stream.Write(tokenBytes); err != nil {
		conn.CloseWithError(1, "write token failed")
		return nil, fmt.Errorf("write token: %w", err)
	}

	cipher, err := crypto.NewStreamCipher(myPriv, theirPub, initiator)
	if err != nil {
		conn.CloseWithError(1, "cipher init failed")
		return nil, fmt.Errorf("create stream cipher: %w", err)
	}

	return &RelayBridge{
		conn:   conn,
		stream: stream,
		cipher: cipher,
	}, nil
}

// WriteFrame encrypts plaintext and sends it as a length-prefixed frame.
// A mutex serializes the encrypt+write pair so concurrent callers cannot
// interleave their ciphertext on the stream.
func (rb *RelayBridge) WriteFrame(plaintext []byte) error {
	rb.writeMu.Lock()
	defer rb.writeMu.Unlock()

	// Allocate a single buffer for the 4-byte length prefix + encrypted payload.
	encLen := rb.cipher.EncryptedLen(len(plaintext))
	frame := make([]byte, 4+encLen)

	// Encrypt directly into frame[4:], avoiding a second allocation.
	n, err := rb.cipher.EncryptTo(frame[4:], plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	binary.BigEndian.PutUint32(frame[:4], uint32(n))

	if _, err := rb.stream.Write(frame[:4+n]); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	return nil
}

// ReadFrame reads and decrypts a length-prefixed frame.
func (rb *RelayBridge) ReadFrame(ctx context.Context) ([]byte, error) {
	// Read 4-byte length prefix.
	var lenBuf [4]byte
	if _, err := io.ReadFull(rb.stream, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read frame length: %w", err)
	}

	frameLen := binary.BigEndian.Uint32(lenBuf[:])
	if frameLen > 1<<20 { // 1 MB max frame
		return nil, fmt.Errorf("frame too large: %d bytes", frameLen)
	}

	data := make([]byte, frameLen)
	if _, err := io.ReadFull(rb.stream, data); err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}

	plaintext, err := rb.cipher.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

// Close closes the underlying QUIC connection.
func (rb *RelayBridge) Close() {
	rb.conn.CloseWithError(0, "closing")
}

// Method returns "relay".
func (rb *RelayBridge) Method() string {
	return "relay"
}
