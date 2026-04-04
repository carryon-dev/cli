package remote

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// DefaultP2PTimeout is how long to attempt P2P candidates before falling back to relay.
const DefaultP2PTimeout = 3 * time.Second

// ConnectParams holds all parameters needed to establish a connection.
type ConnectParams struct {
	EphemeralPrivKey []byte
	EphemeralPubKey  []byte
	RemotePubKey     []byte
	TheirCandidates  []Candidate
	RelayAddr        string // host:port
	PairingToken     string
	IsInitiator      bool
	P2PTimeout       time.Duration // 0 means use DefaultP2PTimeout
	SkipRelayTLS     bool          // skip TLS verification for relay (user-controlled via config)
}

// ConnectionManager orchestrates connection attempts: tries P2P candidates
// in parallel and falls back to relay if P2P fails or times out.
type ConnectionManager struct {
	transport *Transport
	p2pMu     sync.Mutex // serialize P2P attempts to avoid accept races
}

// NewConnectionManager creates a ConnectionManager using the given transport.
func NewConnectionManager(transport *Transport) *ConnectionManager {
	return &ConnectionManager{transport: transport}
}

// Connect attempts a P2P connection to the remote peer (if candidates are
// provided) and falls back to a relay connection on timeout or failure.
// It returns the first successfully established Bridge.
func (cm *ConnectionManager) Connect(ctx context.Context, params ConnectParams) (Bridge, error) {
	timeout := params.P2PTimeout
	if timeout == 0 {
		timeout = DefaultP2PTimeout
	}

	if len(params.TheirCandidates) > 0 {
		p2pCtx, p2pCancel := context.WithTimeout(ctx, timeout)
		defer p2pCancel()

		bridge, err := cm.tryP2P(p2pCtx, params)
		if err == nil {
			return bridge, nil
		}
		// P2P failed or timed out - fall through to relay.
	}

	if params.RelayAddr == "" {
		return nil, fmt.Errorf("P2P failed and no relay configured")
	}

	bridge, err := NewRelayBridge(ctx, params.RelayAddr, params.PairingToken,
		params.EphemeralPrivKey, params.RemotePubKey, params.IsInitiator, params.SkipRelayTLS)
	if err != nil {
		return nil, fmt.Errorf("relay fallback: %w", err)
	}
	return bridge, nil
}

// bridgeResult is the outcome of a full dial+bridge or accept+bridge attempt.
type bridgeResult struct {
	bridge Bridge
	conn   *quic.Conn // non-nil on success, for cleanup of losers
	err    error
}

// tryP2P races all P2P candidates (dial each candidate + accept incoming)
// and returns the first successfully established P2PBridge.
//
// Each candidate and the accept path run entirely in their own goroutine,
// including the NewP2PBridge handshake, so a slow or stalled connection
// never blocks the others from being tried.
//
// The mutex ensures only one P2P attempt runs at a time. This prevents the
// accept goroutine started here from stealing an incoming connection that
// belongs to a concurrent tryP2P call (which would cause a key mismatch).
// Relay fallback still works concurrently since it does not hold this lock.
func (cm *ConnectionManager) tryP2P(ctx context.Context, params ConnectParams) (Bridge, error) {
	cm.p2pMu.Lock()
	defer cm.p2pMu.Unlock()

	// One goroutine per candidate plus one accept goroutine.
	numGoroutines := len(params.TheirCandidates) + 1
	bridges := make(chan bridgeResult, numGoroutines)

	// tryBridge dials or uses an already-accepted conn, then sets up a P2PBridge.
	// It sends exactly one bridgeResult on bridges.
	tryBridge := func(conn *quic.Conn, isInitiator bool, method string) {
		bridge, err := NewP2PBridge(ctx, conn, params.EphemeralPrivKey, params.RemotePubKey, isInitiator, method)
		if err != nil {
			conn.CloseWithError(1, "bridge setup failed")
			bridges <- bridgeResult{err: err}
			return
		}
		bridges <- bridgeResult{bridge: bridge, conn: conn}
	}

	// Launch accept goroutine - the peer may dial us.
	go func() {
		conn, err := cm.transport.Accept(ctx)
		if err != nil {
			bridges <- bridgeResult{err: err}
			return
		}
		// The application-level IsInitiator flag determines stream and cipher
		// direction regardless of which side dialed the QUIC connection.
		// The client (IsInitiator=true) always opens the stream; the server
		// (IsInitiator=false) always accepts it.
		tryBridge(conn, params.IsInitiator, "lan")
	}()

	// Launch a dial+bridge goroutine per candidate.
	for _, c := range params.TheirCandidates {
		c := c // capture loop variable
		go func() {
			if err := validateCandidate(c); err != nil {
				bridges <- bridgeResult{err: fmt.Errorf("candidate rejected: %w", err)}
				return
			}
			addr := fmt.Sprintf("%s:%d", c.Addr, c.Port)
			conn, err := cm.transport.Dial(ctx, addr)
			if err != nil {
				bridges <- bridgeResult{err: err}
				return
			}
			tryBridge(conn, params.IsInitiator, c.Type)
		}()
	}

	// Collect results until we get a usable bridge or all goroutines fail.
	failCount := 0
	for failCount < numGoroutines {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("P2P timeout: %w", ctx.Err())

		case res := <-bridges:
			if res.err != nil {
				failCount++
				continue
			}

			// Winner found. Drain remaining goroutines in the background.
			remaining := numGoroutines - failCount - 1
			go func(n int) {
				for i := 0; i < n; i++ {
					r := <-bridges
					if r.conn != nil {
						r.conn.CloseWithError(0, "lost race")
					}
				}
			}(remaining)

			return res.bridge, nil
		}
	}

	return nil, fmt.Errorf("all P2P attempts failed")
}

// validateCandidate checks that a P2P candidate has a routable IP and valid port.
// Private IPs (RFC 1918) are allowed because LAN P2P connections use them legitimately.
// Only loopback, multicast, and unspecified (0.0.0.0) addresses are rejected.
func validateCandidate(c Candidate) error {
	ip := net.ParseIP(c.Addr)
	if ip == nil {
		return fmt.Errorf("invalid IP: %s", c.Addr)
	}
	if ip.IsLoopback() {
		return fmt.Errorf("rejected loopback IP: %s", c.Addr)
	}
	if ip.IsMulticast() {
		return fmt.Errorf("rejected multicast IP: %s", c.Addr)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("rejected unspecified IP: %s", c.Addr)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Port)
	}
	return nil
}
