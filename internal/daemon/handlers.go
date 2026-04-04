package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/remote"
)

// SignalingHandlers contains the extracted handler logic for signaling messages.
// This allows the handlers to be tested independently of the daemon startup.
type SignalingHandlers struct {
	Logger       *logging.Logger
	DeviceID     string
	DeviceName   string
	TeamID       string
	TeamName     string
	PrivateKey   []byte
	RemoteStates *sync.Map // teamID -> *remote.RemoteState
	BroadcastFn  func(method string, params map[string]any)
	ListSessions func() []backend.Session
	SendMessage  func(msgType string, payload any) error

	// For remote session creation
	CreateSession          func(opts backend.CreateOpts) (backend.Session, error)
	createResponseChannels sync.Map // requestID -> chan remote.SessionCreateResponseMsg

	// For connect flow
	AttachSession  func(sessionID string) (backend.StreamHandle, error)
	answerChannels sync.Map // requestID -> chan remote.ConnectAnswerResponseMsg

	// connectMu serializes concurrent RemoteAttachSession calls so that only
	// one connect.request is in-flight at a time. This prevents the race where
	// two concurrent callers both wait on ConnectAnswerCh and one receives the
	// other's answer. The signaling DO does not include the target session/device
	// in connect.answer, so per-request dispatch is not possible without protocol
	// changes - serialization is the simplest correct fix.
	connectMu sync.Mutex

	// connectSem limits the number of concurrent goroutines spawned by
	// HandleConnectOffer to prevent unbounded resource consumption.
	connectSem chan struct{}

	// Ctx is cancelled when the daemon shuts down, bounding the lifetime of
	// goroutines spawned by connect handlers.
	Ctx context.Context

	// Transport is the QUIC transport for P2P connections (nil = relay only).
	Transport *remote.Transport

	// SkipRelayTLS skips TLS certificate verification for relay connections.
	// Controlled by the local user's config (remote.relay_tls_verify).
	SkipRelayTLS bool

	// PublishSessions re-publishes local sessions to signaling.
	// Called when recipients change (new device online, recipients update).
	PublishSessions func()

	// SaveToken is called when the server rotates the session token.
	// The callback should persist the new token to disk.
	SaveToken func(newToken string)
}

// HandleInitialState processes the initial.state message from the Team DO.
// It populates RemoteState with devices, recipients, and cached sessions.
func (h *SignalingHandlers) HandleInitialState(payload json.RawMessage) {
	var msg remote.InitialStateMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		h.Logger.Warn("remote", fmt.Sprintf("Failed to parse initial.state: %v", err))
		return
	}

	rs := remote.NewRemoteState(h.TeamID, h.TeamName)

	for _, d := range msg.Devices {
		pubKey, _ := base64.StdEncoding.DecodeString(d.PublicKey)
		rs.SetDevice(&remote.RemoteDevice{
			ID: d.ID, Name: d.Name, AccountID: d.AccountID, AccountName: d.AccountName,
			PublicKey: pubKey, Online: d.Online, LastSeen: d.LastSeen,
		})
	}

	recipients := make(map[string][]byte, len(msg.Recipients))
	for devID, b64Key := range msg.Recipients {
		key, _ := base64.StdEncoding.DecodeString(b64Key)
		recipients[devID] = key
	}
	rs.SetRecipients(recipients)

	for _, entry := range msg.SessionCache {
		dev := rs.Device(entry.DeviceID)
		if dev == nil {
			continue
		}
		sessions, blobTS, err := remote.DecryptSessionBlob(
			entry.EncryptedBlob, h.DeviceID, h.PrivateKey, dev.PublicKey,
		)
		if err != nil {
			h.Logger.Warn("remote", fmt.Sprintf("Failed to decrypt sessions from %s: %v", entry.DeviceID, err))
			continue
		}
		// Record the timestamp for future replay detection (initial.state
		// blobs are cached by the server, so we accept them unconditionally
		// to seed the baseline).
		rs.CheckBlobTimestamp(entry.DeviceID, blobTS)
		rs.SetSessions(entry.DeviceID, sessions)
	}

	h.RemoteStates.Store(h.TeamID, rs)

	if h.BroadcastFn != nil {
		h.BroadcastFn("remote.updated", nil)
		// Broadcast granular events for each device in the initial state
		for _, d := range msg.Devices {
			if d.Online {
				h.BroadcastFn("remote.device.online", map[string]any{
					"device_id": d.ID, "device_name": d.Name,
					"account_name": d.AccountName, "team_id": h.TeamID,
				})
			}
		}
	}

	// Publish our own sessions
	if h.ListSessions != nil && h.SendMessage != nil {
		localSessions := h.ListSessions()
		blob, err := remote.BuildSessionBlob(localSessions, h.DeviceID, h.DeviceName, h.PrivateKey, recipients)
		if err != nil {
			h.Logger.Warn("remote", fmt.Sprintf("Failed to build session blob: %v", err))
			return
		}
		if blob != "" {
			_ = h.SendMessage("sessions.update", remote.SessionsUpdateMsg{EncryptedBlob: blob})
		}
	}

	// Save rotated session token if the server provided one.
	if msg.RotatedToken != "" && h.SaveToken != nil {
		h.SaveToken(msg.RotatedToken)
	}
}

// HandleSessionsUpdated processes a sessions.updated message from another device.
func (h *SignalingHandlers) HandleSessionsUpdated(payload json.RawMessage) {
	var msg remote.SessionsUpdatedMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	// Use the authenticated public key from RemoteState, not the self-reported one in the message
	var senderPub []byte
	if val, ok := h.RemoteStates.Load(h.TeamID); ok {
		rs := val.(*remote.RemoteState)
		dev := rs.Device(msg.DeviceID)
		if dev != nil {
			senderPub = dev.PublicKey
		}
	}
	if senderPub == nil {
		h.Logger.Warn("remote", fmt.Sprintf("sessions.updated from unknown device %s", msg.DeviceID))
		return
	}

	sessions, blobTS, err := remote.DecryptSessionBlob(msg.EncryptedBlob, h.DeviceID, h.PrivateKey, senderPub)
	if err != nil {
		h.Logger.Warn("remote", fmt.Sprintf("Failed to decrypt sessions from %s: %v", msg.DeviceID, err))
		return
	}

	if val, ok := h.RemoteStates.Load(h.TeamID); ok {
		rs := val.(*remote.RemoteState)
		if !rs.CheckBlobTimestamp(msg.DeviceID, blobTS) {
			h.Logger.Warn("remote", fmt.Sprintf("Rejecting replayed session blob from %s (stale timestamp)", msg.DeviceID))
			return
		}
		rs.SetSessions(msg.DeviceID, sessions)
	}

	if h.BroadcastFn != nil {
		h.BroadcastFn("remote.sessions.updated", map[string]any{
			"device_id": msg.DeviceID, "team_id": h.TeamID,
		})
		h.BroadcastFn("remote.updated", nil)
	}
}

// HandleDevicePresence processes device.presence (online/offline) messages.
func (h *SignalingHandlers) HandleDevicePresence(payload json.RawMessage) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return
	}

	online, _ := raw["online"].(bool)
	deviceId, _ := raw["device_id"].(string)

	deviceName, _ := raw["device_name"].(string)
	accountName, _ := raw["account_name"].(string)

	if val, ok := h.RemoteStates.Load(h.TeamID); ok {
		rs := val.(*remote.RemoteState)
		if online {
			accountId, _ := raw["account_id"].(string)
			publicKeyB64, _ := raw["public_key"].(string)
			pubKey, _ := base64.StdEncoding.DecodeString(publicKeyB64)

			// Only accept pubkey if device has no existing key established.
			// Pubkeys are set authoritatively via initial.state; presence
			// events should only update online status.
			existingDev := rs.Device(deviceId)
			if existingDev != nil && len(existingDev.PublicKey) > 0 {
				rs.SetDeviceOnlineStatus(deviceId, deviceName, accountId, accountName)
			} else {
				rs.SetDeviceOnline(deviceId, deviceName, accountId, accountName, pubKey)
			}
		} else {
			lastSeen, _ := raw["last_seen"].(string)
			rs.SetDeviceOffline(deviceId, lastSeen)
		}
	}

	if h.BroadcastFn != nil {
		if online {
			h.BroadcastFn("remote.device.online", map[string]any{
				"device_id": deviceId, "device_name": deviceName,
				"account_name": accountName, "team_id": h.TeamID,
			})
		} else {
			h.BroadcastFn("remote.device.offline", map[string]any{
				"device_id": deviceId, "device_name": deviceName,
				"team_id": h.TeamID,
			})
		}
		h.BroadcastFn("remote.updated", nil)
	}

	// When a new device comes online, re-publish our sessions so they can see them.
	if online && h.PublishSessions != nil {
		go h.PublishSessions()
	}
}

// HandleRecipientsUpdate processes recipients.update messages (tag changes).
func (h *SignalingHandlers) HandleRecipientsUpdate(payload json.RawMessage) {
	var msg remote.RecipientsUpdateMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	recipients := make(map[string][]byte, len(msg.Recipients))
	for devID, b64Key := range msg.Recipients {
		key, _ := base64.StdEncoding.DecodeString(b64Key)
		recipients[devID] = key
	}

	if val, ok := h.RemoteStates.Load(h.TeamID); ok {
		rs := val.(*remote.RemoteState)
		rs.SetRecipients(recipients)
	}

	// Re-publish with new recipients
	if h.ListSessions != nil && h.SendMessage != nil {
		localSessions := h.ListSessions()
		blob, err := remote.BuildSessionBlob(localSessions, h.DeviceID, h.DeviceName, h.PrivateKey, recipients)
		if err != nil {
			return
		}
		if blob != "" {
			_ = h.SendMessage("sessions.update", remote.SessionsUpdateMsg{EncryptedBlob: blob})
		}
	}
}

// HandleSessionCreateRequest processes a session.create.request from another device.
// This device is the TARGET - it creates the session locally and responds.
//
// Security: the signaling server enforces access control before delivering this
// message. It uses the sender's authenticated WebSocket identity (not the claimed
// from_device_id) and checks visibility via resolveVisibleDevices() - which
// requires the sender to have tag-based access or a direct session share granted
// by the device owner or a team admin. Messages from unauthorized devices are
// silently dropped server-side and never reach this handler.
//
// The Name, Cwd, and Command fields are intentional - remote session creation
// with a specific working directory and command is a core feature. The check
// below is defense-in-depth: it verifies the sender is in our local RemoteState
// (populated from the server's initial.state and presence events).
func (h *SignalingHandlers) HandleSessionCreateRequest(payload json.RawMessage) {
	var msg remote.SessionCreateForwardMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	if h.CreateSession == nil || h.SendMessage == nil {
		return
	}

	// Defense-in-depth: verify sender is a known team member in our local state.
	// Primary access control is enforced by the signaling server.
	if msg.FromDeviceID == "" {
		h.Logger.Warn("remote", "session.create.request rejected: empty FromDeviceID")
		return
	}
	if rs, ok := h.RemoteStates.Load(h.TeamID); ok {
		state := rs.(*remote.RemoteState)
		if state.Device(msg.FromDeviceID) == nil {
			h.Logger.Warn("remote", "session.create.request from unknown device: "+msg.FromDeviceID)
			return
		}
	} else {
		h.Logger.Warn("remote", "session.create.request: no remote state for team")
		return
	}

	opts := backend.CreateOpts{
		Name:    msg.Name,
		Cwd:     msg.Cwd,
		Command: msg.Command,
	}

	h.Logger.Info("remote", fmt.Sprintf("Remote session create from device %s: name=%q cwd=%q",
		msg.FromDeviceID, msg.Name, msg.Cwd))
	h.Logger.Debug("remote", fmt.Sprintf("Remote session create command=%q", msg.Command))

	sess, err := h.CreateSession(opts)

	resp := map[string]any{
		"type":             "session.create.response",
		"request_id":       msg.RequestID,
		"target_device_id": h.DeviceID,
	}
	if err != nil {
		resp["error"] = err.Error()
	} else {
		resp["session_id"] = sess.ID
	}

	_ = h.SendMessage("session.create.response", resp)
}

// HandleSessionCreateResponse processes a session.create.response from the target device.
func (h *SignalingHandlers) HandleSessionCreateResponse(payload json.RawMessage) {
	var msg remote.SessionCreateResponseMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	if ch, ok := h.createResponseChannels.Load(msg.RequestID); ok {
		select {
		case ch.(chan remote.SessionCreateResponseMsg) <- msg:
		default:
		}
	}
}

// HandleConnectOffer processes a connect.offer from signaling.
// This device is the HOST - it accepts the connection, establishes a bridge,
// and bridges it to the local session.
func (h *SignalingHandlers) HandleConnectOffer(payload json.RawMessage) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		h.Logger.Warn("remote", fmt.Sprintf("Failed to parse connect.offer: %v", err))
		return
	}

	if h.AttachSession == nil || h.SendMessage == nil {
		return
	}

	connectionId, _ := raw["connection_id"].(string)
	relayAddr, _ := raw["relay_url"].(string)
	pairingToken, _ := raw["pairing_token"].(string)
	targetSessionId, _ := raw["target_session_id"].(string)

	// Validate relay URL against trusted domains to prevent a compromised
	// signaling server from redirecting connections to an attacker-controlled
	// relay. E2E encryption protects content, but an untrusted relay can
	// observe metadata and selectively drop traffic.
	// TODO: when self-hosted relays are supported, make this configurable
	// via a "remote.trusted_relays" setting.
	if !isTrustedRelayAddr(relayAddr) {
		h.Logger.Warn("remote", "connect.offer: untrusted relay URL: "+relayAddr)
		return
	}

	// Decode requester's ephemeral pubkey
	var requesterPubkey []byte
	if rpk, ok := raw["requester_pubkey"].(string); ok {
		requesterPubkey, _ = base64.StdEncoding.DecodeString(rpk)
	}
	// Only accept base64-encoded pubkey (the signaling server always sends base64).
	// A JSON array fallback was removed to avoid an inconsistent parsing surface.

	// Extract requester's P2P candidates (snake_case from the DO)
	var requesterCandidates []remote.Candidate
	if rawCandidates, ok := raw["requester_candidates"].([]any); ok {
		for _, rc := range rawCandidates {
			if m, ok := rc.(map[string]any); ok {
				c := remote.Candidate{}
				c.Type, _ = m["type"].(string)
				c.Addr, _ = m["addr"].(string)
				if p, ok := m["port"].(float64); ok {
					c.Port = int(p)
				}
				requesterCandidates = append(requesterCandidates, c)
			}
		}
	}

	// Validate requester pubkey before leaking our STUN address or candidates
	if len(requesterPubkey) != 32 {
		h.Logger.Warn("remote", "connect.offer: invalid requester pubkey length")
		return
	}

	// Verify requester pubkey matches the specific sending device.
	// The signaling server always injects from_device_id into forwarded offers.
	fromDeviceID, _ := raw["from_device_id"].(string)
	if fromDeviceID == "" {
		h.Logger.Warn("remote", "connect.offer: missing from_device_id")
		return
	}
	if rs, ok := h.RemoteStates.Load(h.TeamID); ok {
		state := rs.(*remote.RemoteState)
		dev := state.Device(fromDeviceID)
		if dev == nil || !bytes.Equal(dev.PublicKey, requesterPubkey) {
			h.Logger.Warn("remote", "connect.offer: pubkey mismatch for device "+fromDeviceID)
			return
		}
	} else {
		h.Logger.Warn("remote", "connect.offer: no remote state available for team")
		return
	}

	if targetSessionId == "" {
		h.Logger.Warn("remote", "connect.offer missing targetSessionId")
		return
	}

	// Generate ephemeral keypair for this connection
	ephPub, ephPriv, err := crypto.GenerateKeypair()
	if err != nil {
		h.Logger.Warn("remote", fmt.Sprintf("Failed to generate ephemeral keypair: %v", err))
		return
	}

	// Gather our own P2P candidates
	var ourCandidates []remote.Candidate
	if h.Transport != nil {
		ourCandidates = h.Transport.LocalCandidates()
	}

	// STUN discovery - extend candidates with public address if reachable
	if h.Transport != nil {
		stunParent := h.Ctx
		if stunParent == nil {
			stunParent = context.Background()
		}
		stunCtx, stunCancel := context.WithTimeout(stunParent, 3*time.Second)
		stunCandidate, err := h.Transport.STUNDiscover(stunCtx, "stun.l.google.com:19302")
		stunCancel()
		if err != nil {
			h.Logger.Info("remote", fmt.Sprintf("STUN discovery failed (P2P may still work via LAN): %v", err))
		} else {
			ourCandidates = append(ourCandidates, stunCandidate)
		}
	}

	// Send connect.answer back with our ephemeral pubkey and candidates
	_ = h.SendMessage("connect.answer", remote.ConnectAnswerMsg{
		ConnectionID:    connectionId,
		EphemeralPubkey: ephPub,
		Candidates:      ourCandidates,
	})

	// Establish bridge and wire to local session (async).
	// Use a per-connection context bounded by the daemon's lifetime so a
	// stalled peer or relay cannot pin the goroutine forever.
	go func() {
		// Limit concurrent connection handling
		if h.connectSem != nil {
			select {
			case h.connectSem <- struct{}{}:
				defer func() { <-h.connectSem }()
			default:
				h.Logger.Warn("remote", "too many concurrent connections, dropping offer")
				return
			}
		}

		parentCtx := h.Ctx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		ctx, cancel := context.WithCancel(parentCtx)
		defer cancel()

		var bridge remote.Bridge
		if h.Transport != nil {
			cm := remote.NewConnectionManager(h.Transport)
			b, err := cm.Connect(ctx, remote.ConnectParams{
				EphemeralPrivKey: ephPriv,
				EphemeralPubKey:  ephPub,
				RemotePubKey:     requesterPubkey,
				TheirCandidates:  requesterCandidates,
				RelayAddr:        relayAddr,
				PairingToken:     pairingToken,
				IsInitiator:      false,
				P2PTimeout:       remote.DefaultP2PTimeout,
				SkipRelayTLS:     h.SkipRelayTLS,
			})
			if err != nil {
				h.Logger.Warn("remote", fmt.Sprintf("Failed to connect: %v", err))
				return
			}
			bridge = b
		} else {
			b, err := remote.NewRelayBridge(ctx, relayAddr, pairingToken, ephPriv, requesterPubkey, false, h.SkipRelayTLS)
			if err != nil {
				h.Logger.Warn("remote", fmt.Sprintf("Failed to connect to relay: %v", err))
				return
			}
			bridge = b
		}
		defer bridge.Close()

		h.Logger.Info("remote", fmt.Sprintf("Connected to requester (%s)", bridge.Method()))

		stream, err := h.AttachSession(targetSessionId)
		if err != nil {
			h.Logger.Warn("remote", fmt.Sprintf("Failed to attach session %s: %v", targetSessionId, err))
			return
		}
		defer stream.Close()

		done := make(chan struct{}, 1)

		// Session output -> bridge
		stream.OnData(func(data []byte) int {
			if err := bridge.WriteFrame(data); err != nil {
				select {
				case done <- struct{}{}:
				default:
				}
			}
			return 0
		})

		// Bridge -> session input
		go func() {
			for {
				frame, err := bridge.ReadFrame(ctx)
				if err != nil {
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
				if err := stream.Write(frame); err != nil {
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
			}
		}()

		select {
		case <-done:
		case <-ctx.Done():
		}
		h.Logger.Info("remote", fmt.Sprintf("Remote connection ended for session %s", targetSessionId))
	}()
}

// RegisterAnswerChannel creates a per-request channel for receiving connect.answer
// messages and stores it keyed by requestID. The caller must call
// UnregisterAnswerChannel when done.
func (h *SignalingHandlers) RegisterAnswerChannel(requestID string) chan remote.ConnectAnswerResponseMsg {
	ch := make(chan remote.ConnectAnswerResponseMsg, 1)
	h.answerChannels.Store(requestID, ch)
	return ch
}

// UnregisterAnswerChannel removes a per-request answer channel.
func (h *SignalingHandlers) UnregisterAnswerChannel(requestID string) {
	h.answerChannels.Delete(requestID)
}

// HandleConnectAnswer processes a connect.answer from signaling.
// Dispatches to the per-request channel registered by RemoteAttachSession.
func (h *SignalingHandlers) HandleConnectAnswer(payload json.RawMessage) {
	var msg remote.ConnectAnswerResponseMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	if msg.RequestID == "" {
		h.Logger.Warn("remote", "connect.answer missing request_id - dropping")
		return
	}

	if ch, ok := h.answerChannels.Load(msg.RequestID); ok {
		select {
		case ch.(chan remote.ConnectAnswerResponseMsg) <- msg:
		default:
		}
	} else {
		h.Logger.Info("remote", fmt.Sprintf("connect.answer for unknown request %q - discarding", msg.RequestID))
	}
}

// isTrustedRelayAddr checks whether a relay address belongs to a trusted domain.
// In dev mode, localhost addresses are allowed for local development.
// In production, only *.relay.carryon.dev is trusted.
func isTrustedRelayAddr(addr string) bool {
	if addr == "" {
		return false
	}

	// Normalize: if it looks like a URL, parse the host; otherwise treat as host:port.
	host := addr
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil {
			host = u.Hostname()
		}
	} else if h, _, found := strings.Cut(addr, ":"); found {
		host = h
	}

	// In dev mode, allow localhost/loopback only for local development.
	if IsDevMode() {
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}

	// Production: only trust *.relay.carryon.dev
	return host == "relay.carryon.dev" || strings.HasSuffix(host, ".relay.carryon.dev")
}
