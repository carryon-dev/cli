package remote

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/crypto"
)

// P2PBridge is an E2E encrypted bidirectional connection over a direct QUIC stream.
// It implements the Bridge interface.
type P2PBridge struct {
	conn    *quic.Conn
	stream  *quic.Stream
	cipher  *crypto.StreamCipher
	method  string
	writeMu sync.Mutex
}

// NewP2PBridge sets up a P2PBridge over an existing QUIC connection.
// If initiator is true, it opens a new stream; otherwise it accepts one.
// myPriv and theirPub are 32-byte X25519 keys used to derive the StreamCipher.
// method is the connection establishment method ("lan" or "p2p").
//
// QUIC streams are not visible to the remote's AcceptStream until at least one
// byte is written. The initiator sends a single zero handshake byte; the
// non-initiator reads and discards it before the bridge is ready.
func NewP2PBridge(ctx context.Context, conn *quic.Conn, myPriv, theirPub []byte, initiator bool, method string) (*P2PBridge, error) {
	var stream *quic.Stream
	var err error

	if initiator {
		stream, err = conn.OpenStreamSync(ctx)
		if err != nil {
			return nil, fmt.Errorf("stream setup: %w", err)
		}
		// Send a handshake byte so the stream becomes visible to AcceptStream.
		if _, err := stream.Write([]byte{0}); err != nil {
			return nil, fmt.Errorf("stream handshake write: %w", err)
		}
	} else {
		stream, err = conn.AcceptStream(ctx)
		if err != nil {
			return nil, fmt.Errorf("stream setup: %w", err)
		}
		// Read and discard the initiator's handshake byte.
		var handshake [1]byte
		if _, err := io.ReadFull(stream, handshake[:]); err != nil {
			return nil, fmt.Errorf("stream handshake read: %w", err)
		}
	}

	cipher, err := crypto.NewStreamCipher(myPriv, theirPub, initiator)
	if err != nil {
		return nil, fmt.Errorf("create stream cipher: %w", err)
	}

	return &P2PBridge{
		conn:   conn,
		stream: stream,
		cipher: cipher,
		method: method,
	}, nil
}

// WriteFrame encrypts plaintext and sends it with a 4-byte big-endian length prefix.
// A mutex serializes the encrypt+write pair so concurrent callers cannot
// interleave their ciphertext on the stream.
func (b *P2PBridge) WriteFrame(plaintext []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	// Single allocation for length prefix + encrypted payload.
	encLen := b.cipher.EncryptedLen(len(plaintext))
	frame := make([]byte, 4+encLen)

	n, err := b.cipher.EncryptTo(frame[4:], plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	binary.BigEndian.PutUint32(frame[:4], uint32(n))

	if _, err := b.stream.Write(frame[:4+n]); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// ReadFrame reads a 4-byte length-prefixed frame, then decrypts and returns it.
func (b *P2PBridge) ReadFrame(ctx context.Context) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(b.stream, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length prefix: %w", err)
	}

	frameLen := binary.BigEndian.Uint32(lenBuf[:])
	if frameLen > 1*1024*1024 { // 1 MiB sanity limit (matches relay bridge)
		return nil, fmt.Errorf("frame too large: %d bytes", frameLen)
	}

	data := make([]byte, frameLen)
	if _, err := io.ReadFull(b.stream, data); err != nil {
		return nil, fmt.Errorf("read frame data: %w", err)
	}

	plaintext, err := b.cipher.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// Close closes the underlying QUIC connection.
func (b *P2PBridge) Close() {
	b.conn.CloseWithError(0, "closing")
}

// Method returns how this connection was established: "lan" or "p2p".
func (b *P2PBridge) Method() string {
	return b.method
}
