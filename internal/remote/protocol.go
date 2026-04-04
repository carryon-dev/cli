package remote

import (
	"encoding/json"
	"fmt"
)

// Candidate represents a network address where a device can be reached for P2P connections.
type Candidate struct {
	Type string `json:"type"` // "lan" or "stun"
	Addr string `json:"addr"` // IP address
	Port int    `json:"port"` // UDP port
}

// SignalMsg is a signaling message. All fields are at the top level (flat format).
// The Type field identifies the message kind, and the rest of the raw JSON
// is passed to the appropriate handler for parsing.
type SignalMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage // the full raw message (set by ParseSignalMsg)
	Error   string          `json:"error,omitempty"`
}

// Daemon/Client -> DO messages

// DeviceAuthMsg is sent by a daemon or client after WebSocket upgrade.
// The session token is sent in the HTTP Authorization header during upgrade,
// not repeated here.
type DeviceAuthMsg struct {
	DeviceID  string `json:"device_id"`
	PublicKey []byte `json:"public_key"`
}

// SessionsUpdateMsg is sent to push an encrypted sessions blob to signaling.
type SessionsUpdateMsg struct {
	EncryptedBlob string `json:"encrypted_sessions"`
}

// ConnectRequestMsg is sent by a client to request a connection to a remote session.
type ConnectRequestMsg struct {
	RequestID       string      `json:"request_id,omitempty"`
	TargetDeviceID  string      `json:"target_device_id"`
	TargetSessionID string      `json:"target_session_id"`
	EphemeralPubkey []byte      `json:"ephemeral_pubkey"`
	Candidates      []Candidate `json:"candidates,omitempty"`
}

// ConnectAnswerMsg is sent by a daemon to answer a connection offer.
type ConnectAnswerMsg struct {
	ConnectionID    string      `json:"connection_id"`
	EphemeralPubkey []byte      `json:"ephemeral_pubkey"`
	Candidates      []Candidate `json:"candidates,omitempty"`
}

// DO -> Daemon/Client messages

// SessionsUpdatedMsg is sent by the relay to deliver another device's updated sessions blob.
type SessionsUpdatedMsg struct {
	DeviceID        string `json:"device_id"`
	SenderPublicKey string `json:"sender_public_key"`
	EncryptedBlob   string `json:"encrypted_blob"`
}

// ConnectOfferMsg is sent by the relay to the target daemon when a client wants to connect.
type ConnectOfferMsg struct {
	ConnectionID        string      `json:"connection_id"`
	RelayURL            string      `json:"relay_url"`
	PairingToken        string      `json:"pairing_token"`
	RequesterPubkey     []byte      `json:"requester_pubkey"`
	RequesterCandidates []Candidate `json:"requester_candidates,omitempty"`
}

// ConnectAnswerResponseMsg is sent by the relay to the requesting client after the daemon answers.
type ConnectAnswerResponseMsg struct {
	ConnectionID        string      `json:"connection_id"`
	RequestID           string      `json:"request_id"`
	RelayURL            string      `json:"relay_url"`
	PairingToken        string      `json:"pairing_token"`
	ResponderPubkey     []byte      `json:"responder_pubkey"`
	ResponderCandidates []Candidate `json:"responder_candidates,omitempty"`
}

// RemoteSession describes a session on a remote device, as seen by a client.
type RemoteSession struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
	Created      int64  `json:"created"`
	LastAttached int64  `json:"last_attached,omitempty"`
	TeamID       string `json:"team_id,omitempty"`
	TeamName     string `json:"team_name,omitempty"`
}

// InitialStateDevice is a device entry in the initial.state message.
type InitialStateDevice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	PublicKey   string `json:"public_key"`
	Online      bool   `json:"online"`
	LastSeen    string `json:"last_seen"`
}

// SessionCacheEntry is a cached encrypted session blob in initial.state.
type SessionCacheEntry struct {
	DeviceID      string `json:"device_id"`
	EncryptedBlob string `json:"encrypted_blob"`
	UpdatedAt     string `json:"updated_at"`
}

// InitialStateMsg is the enhanced initial.state message from the Team DO.
type InitialStateMsg struct {
	Devices      []InitialStateDevice `json:"devices"`
	SessionCache []SessionCacheEntry  `json:"session_cache"`
	Recipients   map[string]string    `json:"recipients"`
	RotatedToken string               `json:"rotated_token,omitempty"`
}

// RecipientsUpdateMsg is sent when tag assignments change.
type RecipientsUpdateMsg struct {
	Recipients map[string]string `json:"recipients"`
}

// SessionCreateRequestMsg is sent to request session creation on a remote device.
type SessionCreateRequestMsg struct {
	RequestID      string `json:"request_id"`
	TargetDeviceID string `json:"target_device_id"`
	Name           string `json:"name,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Command        string `json:"command,omitempty"`
}

// SessionCreateResponseMsg is the response to a session create request.
type SessionCreateResponseMsg struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SessionCreateForwardMsg is the create request as forwarded to the target device.
type SessionCreateForwardMsg struct {
	RequestID    string `json:"request_id"`
	FromDeviceID string `json:"from_device_id"`
	Name         string `json:"name,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Command      string `json:"command,omitempty"`
}

// NewSignalMsg builds a flat signaling message with "type" at the top level.
// The payload fields are merged into the top-level object alongside "type".
func NewSignalMsg(msgType string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	// Fast path: if payload marshals to a JSON object, splice "type" in
	// via byte manipulation instead of unmarshal + re-marshal.
	if len(raw) > 0 && raw[0] == '{' {
		typeJSON, _ := json.Marshal(msgType)
		if len(raw) == 2 {
			// Empty object "{}" - just build {"type": ...}
			return append(append([]byte(`{"type":`), typeJSON...), '}'), nil
		}
		// Insert "type":... after the opening brace: {"type":..., <rest>
		out := make([]byte, 0, len(raw)+len(typeJSON)+10)
		out = append(out, `{"type":`...)
		out = append(out, typeJSON...)
		out = append(out, ',')
		out = append(out, raw[1:]...) // skip the opening '{'
		return out, nil
	}

	// Payload isn't an object - wrap it as {"type": ..., "data": ...}
	return json.Marshal(map[string]any{"type": msgType, "data": payload})
}

// ParseSignalMsg parses a flat signaling message.
// Extracts "type" and passes the full raw message as Payload for handlers to parse.
func ParseSignalMsg(data []byte) (SignalMsg, error) {
	var envelope struct {
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return SignalMsg{}, fmt.Errorf("parse signal message: %w", err)
	}
	return SignalMsg{
		Type:    envelope.Type,
		Payload: json.RawMessage(data),
		Error:   envelope.Error,
	}, nil
}
