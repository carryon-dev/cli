package remote

import "context"

// Bridge is an E2E encrypted bidirectional connection to a remote device.
// Implementations include relay-based and direct P2P connections.
type Bridge interface {
	// WriteFrame encrypts and sends a plaintext frame.
	WriteFrame(plaintext []byte) error

	// ReadFrame reads and decrypts the next frame.
	ReadFrame(ctx context.Context) ([]byte, error)

	// Close closes the underlying connection.
	Close()

	// Method returns how this connection was established: "lan", "p2p", or "relay".
	Method() string
}
