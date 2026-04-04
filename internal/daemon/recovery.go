package daemon

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/state"
)

// RecoveryResult holds the counts from a session recovery pass.
type RecoveryResult struct {
	Recovered int
	Cleaned   int
}

// RecoverSessions iterates saved sessions and checks which native sessions
// still have a live holder socket. Sessions whose holder is still running
// are kept (Recovered); sessions whose holder socket is unreachable are
// removed from state (Cleaned).
//
// Non-native sessions (e.g. tmux) are skipped -- their backend handles recovery.
func RecoverSessions(sessionState *state.SessionState, logger *logging.Logger, baseDir string) RecoveryResult {
	saved := sessionState.GetAll()
	var result RecoveryResult

	for _, session := range saved {
		if session.Backend != "native" {
			continue
		}

		sockPath := holder.SocketPath(baseDir, session.ID)
		conn, err := holder.Dial(sockPath)
		if err != nil {
			// Holder is gone -- clean up the session.
			sessionState.Remove(session.ID)
			result.Cleaned++
			logger.Debug("recovery", fmt.Sprintf("Removed stale session (holder unreachable): %s", session.ID))
			continue
		}

		// Query the holder for live status and update session state.
		status, err := queryHolderStatus(conn)
		conn.Close()
		if err != nil {
			// Holder is alive but status query failed - still recover, just log a warning.
			logger.Warn("recovery", fmt.Sprintf("Holder alive but status query failed for session %s: %v", session.ID, err))
		} else {
			session.PID = int(status.PID)
			// Subtract 1 because the recovery probe connection itself is
			// counted as a client by the holder. It will be closed after
			// this query, so the real count is one less.
			clients := int(status.ClientCount) - 1
			if clients < 0 {
				clients = 0
			}
			session.AttachedClients = clients
			sessionState.Save(session)
			logger.Debug("recovery", fmt.Sprintf("Updated status for session %s: pid=%d clients=%d", session.ID, status.PID, status.ClientCount))
		}

		result.Recovered++
		logger.Info("recovery", fmt.Sprintf("Holder alive for session %s", session.ID))
	}

	return result
}

// queryHolderStatus connects to a holder and retrieves its current status.
// It reads the handshake and scrollback (required by the protocol), sends a
// FrameStatusRequest, and waits for a FrameStatusResponse, skipping any
// FrameData frames that arrive in between. A 3s read deadline is used
// throughout to avoid blocking on unresponsive holders.
func queryHolderStatus(conn net.Conn) (holder.StatusResponse, error) {
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	// Read handshake bytes.
	var buf []byte
	tmp := make([]byte, 32*1024)
	var hs holder.Handshake
	var rest []byte

	for {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}

		var decErr error
		hs, rest, decErr = holder.DecodeHandshake(buf)
		if decErr == nil {
			break
		}

		if rerr != nil {
			return holder.StatusResponse{}, fmt.Errorf("read handshake: %w", rerr)
		}
	}

	// Read and discard the scrollback bytes.
	const maxScrollback = 512 * 1024 // 512 KB sanity limit
	if hs.ScrollbackLen > maxScrollback {
		return holder.StatusResponse{}, fmt.Errorf("unreasonably large scrollback: %d bytes", hs.ScrollbackLen)
	}
	scrollbackLen := int(hs.ScrollbackLen)
	for len(rest) < scrollbackLen {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			rest = append(rest, tmp[:n]...)
		}
		if rerr != nil && rerr != io.EOF {
			return holder.StatusResponse{}, fmt.Errorf("read scrollback: %w", rerr)
		}
	}
	// Any bytes beyond scrollback are overflow frames - pass them through.
	overflow := rest[scrollbackLen:]

	// Send FrameStatusRequest.
	req := holder.EncodeFrame(holder.FrameStatusRequest, nil)
	if _, err := conn.Write(req); err != nil {
		return holder.StatusResponse{}, fmt.Errorf("send status request: %w", err)
	}

	// Read frames until we get a FrameStatusResponse, skipping FrameData frames.
	frameBuf := make([]byte, len(overflow))
	copy(frameBuf, overflow)

	for {
		typ, payload, rest2, ferr := holder.DecodeFrame(frameBuf)
		if ferr == nil {
			frameBuf = rest2
			if typ == holder.FrameStatusResponse {
				return holder.DecodeStatusResponse(payload)
			}
			// Skip other frame types (e.g. FrameData).
			continue
		}

		// Need more data.
		n, rerr := conn.Read(tmp)
		if n > 0 {
			frameBuf = append(frameBuf, tmp[:n]...)
		}
		if rerr != nil {
			return holder.StatusResponse{}, fmt.Errorf("read status response: %w", rerr)
		}
	}
}
