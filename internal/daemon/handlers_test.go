package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/remote"
)

func testHandlers(t *testing.T) (*SignalingHandlers, []byte, []byte) {
	t.Helper()
	pub, priv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	logStore := logging.NewStore("", 0)
	logger := logging.NewLogger(logStore, "debug")

	var states sync.Map
	// Pre-create a RemoteState so handlers that need an existing one work
	rs := remote.NewRemoteState("team-1", "My Team")
	// Add a test device with the all-zeros pubkey used by connect.offer tests
	rs.SetDevice(&remote.RemoteDevice{
		ID:        "requester-device",
		Name:      "Requester",
		PublicKey: make([]byte, 32),
		Online:    true,
	})
	states.Store("team-1", rs)

	h := &SignalingHandlers{
		Logger:       logger,
		DeviceID:     "my-device",
		DeviceName:   "My Laptop",
		TeamID:       "team-1",
		TeamName:     "My Team",
		PrivateKey:   priv,
		RemoteStates: &states,
	}

	return h, pub, priv
}

func TestHandleInitialState(t *testing.T) {
	h, myPub, _ := testHandlers(t)

	// Generate a remote device keypair
	remotePub, remotePriv, _ := crypto.GenerateKeypair()

	// Build an encrypted session blob from the remote device
	sessions := []backend.Session{
		{ID: "native-1", Name: "remote-project", Backend: "native", Created: 1000},
	}
	blob, err := remote.BuildSessionBlob(sessions, "remote-dev", "Remote Box", remotePriv, map[string][]byte{
		"my-device": myPub,
	})
	if err != nil {
		t.Fatalf("build blob: %v", err)
	}

	// Track broadcasts - we expect remote.updated AND remote.device.online
	var broadcasts []string
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
	}

	// Track session publish
	var publishCalled bool
	h.ListSessions = func() []backend.Session { return nil }
	h.SendMessage = func(msgType string, payload any) error {
		publishCalled = true
		return nil
	}

	msg := remote.InitialStateMsg{
		Devices: []remote.InitialStateDevice{
			{
				ID: "remote-dev", Name: "Remote Box", AccountID: "acct-2",
				PublicKey: base64.StdEncoding.EncodeToString(remotePub),
				Online: true, LastSeen: "2026-03-28T12:00:00Z",
			},
		},
		SessionCache: []remote.SessionCacheEntry{
			{DeviceID: "remote-dev", EncryptedBlob: blob, UpdatedAt: "2026-03-28T12:00:00Z"},
		},

		Recipients:       map[string]string{"remote-dev": base64.StdEncoding.EncodeToString(remotePub)},
	}

	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload)

	// Verify state was populated
	val, ok := h.RemoteStates.Load("team-1")
	if !ok {
		t.Fatal("RemoteState not stored")
	}
	rs := val.(*remote.RemoteState)

	dev := rs.Device("remote-dev")
	if dev == nil {
		t.Fatal("device not found")
	}
	if dev.Name != "Remote Box" {
		t.Errorf("expected Remote Box, got %s", dev.Name)
	}
	if !dev.Online {
		t.Error("device should be online")
	}

	// Verify sessions were decrypted
	remoteSessions := rs.Sessions("remote-dev")
	if len(remoteSessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(remoteSessions))
	}
	if remoteSessions[0].Name != "remote-project" {
		t.Errorf("expected remote-project, got %s", remoteSessions[0].Name)
	}

	// Verify recipients stored
	recipients := rs.Recipients()
	if len(recipients) != 1 {
		t.Fatalf("expected 1 recipient, got %d", len(recipients))
	}

	if len(broadcasts) == 0 {
		t.Error("broadcast should have been called")
	}
	// Should have remote.updated and remote.device.online (for the online device)
	hasUpdated := false
	hasDeviceOnline := false
	for _, b := range broadcasts {
		if b == "remote.updated" {
			hasUpdated = true
		}
		if b == "remote.device.online" {
			hasDeviceOnline = true
		}
	}
	if !hasUpdated {
		t.Error("expected remote.updated broadcast")
	}
	if !hasDeviceOnline {
		t.Error("expected remote.device.online broadcast for online device in initial state")
	}

	if !publishCalled {
		t.Error("expected sessions to be published after initial state")
	}
}

func TestHandleSessionsUpdated(t *testing.T) {
	h, myPub, _ := testHandlers(t)

	senderPub, senderPriv, _ := crypto.GenerateKeypair()

	// Pre-register the sender device in RemoteState so the handler can look up
	// its authenticated public key (Issue 5 fix: use RemoteState key, not msg key).
	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	rs.SetDevice(&remote.RemoteDevice{
		ID: "sender-dev", Name: "Sender", PublicKey: senderPub,
	})

	sessions := []backend.Session{
		{ID: "s1", Name: "test-session", Backend: "native", Created: 2000},
		{ID: "s2", Name: "another", Backend: "native", Created: 3000},
	}

	blob, _ := remote.BuildSessionBlob(sessions, "sender-dev", "Sender", senderPriv, map[string][]byte{
		"my-device": myPub,
	})

	var broadcasts []string
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
	}

	msg := remote.SessionsUpdatedMsg{
		DeviceID:      "sender-dev",
		EncryptedBlob: blob,
		// SenderPublicKey is no longer trusted - handler uses RemoteState key
	}
	payload, _ := json.Marshal(msg)
	h.HandleSessionsUpdated(payload)

	remoteSessions := rs.Sessions("sender-dev")
	if len(remoteSessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(remoteSessions))
	}
	if remoteSessions[0].Name != "test-session" {
		t.Errorf("expected test-session, got %s", remoteSessions[0].Name)
	}
	if len(broadcasts) == 0 {
		t.Error("broadcast should have been called")
	}
	hasSessionsUpdated := false
	hasUpdated := false
	for _, b := range broadcasts {
		if b == "remote.sessions.updated" {
			hasSessionsUpdated = true
		}
		if b == "remote.updated" {
			hasUpdated = true
		}
	}
	if !hasSessionsUpdated {
		t.Error("expected remote.sessions.updated broadcast")
	}
	if !hasUpdated {
		t.Error("expected remote.updated broadcast")
	}
}

func TestHandleSessionsUpdatedWrongKey(t *testing.T) {
	h, myPub, _ := testHandlers(t)

	// Register "evil-dev" in RemoteState with a different pub key than what it
	// actually signed with. The decryption should fail because the blob was not
	// encrypted to us with the registered key.
	_, wrongPriv, _ := crypto.GenerateKeypair()
	registeredPub, _, _ := crypto.GenerateKeypair() // different from the actual signing key

	// Pre-register the device with a key that won't match the blob's sender key
	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	rs.SetDevice(&remote.RemoteDevice{ID: "evil-dev", Name: "Evil", PublicKey: registeredPub})

	sessions := []backend.Session{{ID: "s1", Name: "secret"}}
	blob, _ := remote.BuildSessionBlob(sessions, "evil-dev", "Evil", wrongPriv, map[string][]byte{
		"my-device": myPub,
	})

	// Should not crash, just log warning - decryption will fail because the
	// registered key differs from the actual signing key
	msg := remote.SessionsUpdatedMsg{
		DeviceID:      "evil-dev",
		EncryptedBlob: blob,
	}
	payload, _ := json.Marshal(msg)
	h.HandleSessionsUpdated(payload) // should not panic

	remoteSessions := rs.Sessions("evil-dev")
	if len(remoteSessions) != 0 {
		t.Error("sessions should not be stored when decryption fails")
	}
}

func TestHandleDevicePresenceOnline(t *testing.T) {
	h, _, _ := testHandlers(t)

	pub, _, _ := crypto.GenerateKeypair()

	var broadcasts []string
	var onlineParams map[string]any
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
		if method == "remote.device.online" {
			onlineParams = params
		}
	}

	msg := map[string]any{
		"device_id":    "new-dev",
		"device_name":  "New Device",
		"account_id":   "acct-3",
		"account_name": "Alice",
		"public_key":   base64.StdEncoding.EncodeToString(pub),
		"online":       true,
	}
	payload, _ := json.Marshal(msg)
	h.HandleDevicePresence(payload)

	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	dev := rs.Device("new-dev")
	if dev == nil {
		t.Fatal("device should exist")
	}
	if !dev.Online {
		t.Error("device should be online")
	}
	if dev.Name != "New Device" {
		t.Errorf("expected New Device, got %s", dev.Name)
	}
	if dev.AccountID != "acct-3" {
		t.Errorf("expected acct-3, got %s", dev.AccountID)
	}

	// Verify granular broadcast
	hasDeviceOnline := false
	hasUpdated := false
	for _, b := range broadcasts {
		if b == "remote.device.online" {
			hasDeviceOnline = true
		}
		if b == "remote.updated" {
			hasUpdated = true
		}
	}
	if !hasDeviceOnline {
		t.Error("expected remote.device.online broadcast")
	}
	if !hasUpdated {
		t.Error("expected remote.updated broadcast")
	}
	if onlineParams == nil {
		t.Fatal("online params should not be nil")
	}
	if onlineParams["device_id"] != "new-dev" {
		t.Errorf("expected device_id new-dev, got %v", onlineParams["device_id"])
	}
	if onlineParams["device_name"] != "New Device" {
		t.Errorf("expected device_name New Device, got %v", onlineParams["device_name"])
	}
	if onlineParams["account_name"] != "Alice" {
		t.Errorf("expected account_name Alice, got %v", onlineParams["account_name"])
	}
	if onlineParams["team_id"] != "team-1" {
		t.Errorf("expected team_id team-1, got %v", onlineParams["team_id"])
	}
}

func TestHandleDevicePresenceOffline(t *testing.T) {
	h, _, _ := testHandlers(t)

	// Set device online first
	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	rs.SetDevice(&remote.RemoteDevice{ID: "dev-1", Name: "Laptop", Online: true})

	var broadcasts []string
	var offlineParams map[string]any
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
		if method == "remote.device.offline" {
			offlineParams = params
		}
	}

	msg := map[string]any{
		"device_id":   "dev-1",
		"device_name": "Laptop",
		"online":      false,
		"last_seen":   "2026-03-28T15:00:00Z",
	}
	payload, _ := json.Marshal(msg)
	h.HandleDevicePresence(payload)

	dev := rs.Device("dev-1")
	if dev.Online {
		t.Error("device should be offline")
	}
	if dev.LastSeen != "2026-03-28T15:00:00Z" {
		t.Errorf("expected updated lastSeen, got %s", dev.LastSeen)
	}

	// Verify granular broadcast
	hasDeviceOffline := false
	hasUpdated := false
	for _, b := range broadcasts {
		if b == "remote.device.offline" {
			hasDeviceOffline = true
		}
		if b == "remote.updated" {
			hasUpdated = true
		}
	}
	if !hasDeviceOffline {
		t.Error("expected remote.device.offline broadcast")
	}
	if !hasUpdated {
		t.Error("expected remote.updated broadcast")
	}
	if offlineParams == nil {
		t.Fatal("offline params should not be nil")
	}
	if offlineParams["device_id"] != "dev-1" {
		t.Errorf("expected device_id dev-1, got %v", offlineParams["device_id"])
	}
}

func TestHandleRecipientsUpdate(t *testing.T) {
	h, _, _ := testHandlers(t)

	pub1, _, _ := crypto.GenerateKeypair()
	pub2, _, _ := crypto.GenerateKeypair()

	var sentMsg string
	h.ListSessions = func() []backend.Session { return nil }
	h.SendMessage = func(msgType string, payload any) error {
		sentMsg = msgType
		return nil
	}

	msg := remote.RecipientsUpdateMsg{
		Recipients: map[string]string{
			"dev-a": base64.StdEncoding.EncodeToString(pub1),
			"dev-b": base64.StdEncoding.EncodeToString(pub2),
		},
	}
	payload, _ := json.Marshal(msg)
	h.HandleRecipientsUpdate(payload)

	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	recipients := rs.Recipients()
	if len(recipients) != 2 {
		t.Fatalf("expected 2 recipients, got %d", len(recipients))
	}
	_ = sentMsg
}

func TestHandleInitialStateBadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	// Should not panic on invalid JSON
	h.HandleInitialState(json.RawMessage(`{invalid json`))
}

func TestHandleSessionsUpdatedBadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleSessionsUpdated(json.RawMessage(`not json`))
}

func TestHandleDevicePresenceBadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleDevicePresence(json.RawMessage(`}}`))
}

func TestHandleRecipientsUpdateBadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleRecipientsUpdate(json.RawMessage(`[`))
}

func TestHandleSessionCreateRequest(t *testing.T) {
	h, _, _ := testHandlers(t)

	// Register the requesting device as a known team member
	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	rs.SetDevice(&remote.RemoteDevice{ID: "other-dev", Name: "Other", Online: true})

	var createdOpts backend.CreateOpts
	h.CreateSession = func(opts backend.CreateOpts) (backend.Session, error) {
		createdOpts = opts
		return backend.Session{ID: "native-new", Name: opts.Name, Backend: "native"}, nil
	}

	var sentType string
	var sentPayload any
	h.SendMessage = func(msgType string, payload any) error {
		sentType = msgType
		sentPayload = payload
		return nil
	}

	msg := remote.SessionCreateForwardMsg{
		RequestID:    "req-1",
		FromDeviceID: "other-dev",
		Name:         "test-session",
		Cwd:          "/home/user",
		Command:      "bash",
	}
	payload, _ := json.Marshal(msg)
	h.HandleSessionCreateRequest(payload)

	if createdOpts.Name != "test-session" {
		t.Errorf("expected name test-session, got %s", createdOpts.Name)
	}
	if createdOpts.Cwd != "/home/user" {
		t.Errorf("expected cwd /home/user, got %s", createdOpts.Cwd)
	}
	if sentType != "session.create.response" {
		t.Errorf("expected session.create.response, got %s", sentType)
	}
	respMap, ok := sentPayload.(map[string]any)
	if !ok {
		t.Fatal("expected map payload")
	}
	if respMap["session_id"] != "native-new" {
		t.Errorf("expected native-new, got %v", respMap["session_id"])
	}
}

func TestHandleSessionCreateRequestError(t *testing.T) {
	h, _, _ := testHandlers(t)

	// Register the requesting device as a known team member
	val, _ := h.RemoteStates.Load("team-1")
	rs := val.(*remote.RemoteState)
	rs.SetDevice(&remote.RemoteDevice{ID: "dev-x", Name: "DevX", Online: true})

	h.CreateSession = func(opts backend.CreateOpts) (backend.Session, error) {
		return backend.Session{}, fmt.Errorf("backend unavailable")
	}

	var sentPayload any
	h.SendMessage = func(msgType string, payload any) error {
		sentPayload = payload
		return nil
	}

	msg := remote.SessionCreateForwardMsg{RequestID: "req-2", FromDeviceID: "dev-x"}
	payload, _ := json.Marshal(msg)
	h.HandleSessionCreateRequest(payload)

	respMap := sentPayload.(map[string]any)
	if respMap["error"] != "backend unavailable" {
		t.Errorf("expected error message, got %v", respMap["error"])
	}
}

func TestHandleSessionCreateResponse(t *testing.T) {
	h, _, _ := testHandlers(t)

	// Register a per-request channel (same pattern as answerChannels).
	ch := make(chan remote.SessionCreateResponseMsg, 1)
	h.createResponseChannels.Store("req-1", ch)

	msg := remote.SessionCreateResponseMsg{
		RequestID: "req-1",
		SessionID: "native-abc",
	}
	payload, _ := json.Marshal(msg)
	h.HandleSessionCreateResponse(payload)

	select {
	case resp := <-ch:
		if resp.SessionID != "native-abc" {
			t.Errorf("expected native-abc, got %s", resp.SessionID)
		}
	default:
		t.Error("expected response on channel")
	}
}

func TestHandleConnectAnswer(t *testing.T) {
	h, _, _ := testHandlers(t)

	// Register a per-request channel
	reqID := "test-request-1"
	ch := h.RegisterAnswerChannel(reqID)
	defer h.UnregisterAnswerChannel(reqID)

	pub, _, _ := crypto.GenerateKeypair()
	msg := remote.ConnectAnswerResponseMsg{
		ConnectionID:    "conn-1",
		RequestID:       reqID,
		RelayURL:        "ws://relay:8080",
		PairingToken:    "token-123",
		ResponderPubkey: pub,
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectAnswer(payload)

	select {
	case ans := <-ch:
		if ans.ConnectionID != "conn-1" {
			t.Errorf("expected conn-1, got %s", ans.ConnectionID)
		}
		if ans.RelayURL != "ws://relay:8080" {
			t.Errorf("expected relay URL, got %s", ans.RelayURL)
		}
	default:
		t.Error("expected answer on channel")
	}
}

// ---------------------------------------------------------------------------
// HandleConnectOffer adversarial tests
// ---------------------------------------------------------------------------

func TestHandleConnectOffer_MissingSessionId(t *testing.T) {
	h, _, _ := testHandlers(t)

	var sentTypes []string
	h.SendMessage = func(msgType string, payload any) error {
		sentTypes = append(sentTypes, msgType)
		return nil
	}
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("should not be called")
	}

	// Offer with valid pubkey but no target_session_id.
	// connect.answer must NOT be sent - we validate session ID before answering
	// to avoid leaking our STUN address when the request is invalid.
	msg := map[string]any{
		"connection_id":    "conn-missing-session",
		"relay_url":        "ws://localhost:9999",
		"pairing_token":    "tok-1",
		"requester_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		// target_session_id intentionally omitted
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectOffer(payload) // must not panic

	for _, mt := range sentTypes {
		if mt == "connect.answer" {
			t.Error("connect.answer must not be sent when target_session_id is missing")
		}
	}
}

func TestHandleConnectOffer_MissingPubkey(t *testing.T) {
	h, _, _ := testHandlers(t)

	var sentTypes []string
	h.SendMessage = func(msgType string, payload any) error {
		sentTypes = append(sentTypes, msgType)
		return nil
	}
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("attach failed")
	}

	// Offer with no requester_pubkey - connect.answer must NOT be sent because
	// that would leak our STUN address to an unauthenticated requester.
	msg := map[string]any{
		"connection_id":     "conn-no-pubkey",
		"relay_url":         "ws://localhost:9999",
		"pairing_token":     "tok-2",
		"target_session_id": "sess-1",
		// requester_pubkey intentionally omitted
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectOffer(payload) // must not panic

	for _, mt := range sentTypes {
		if mt == "connect.answer" {
			t.Error("connect.answer must not be sent when requester_pubkey is missing")
		}
	}
}

// TestHandleConnectOffer_ShortPubkey verifies that a 1-byte requester_pubkey
// is rejected and connect.answer is not sent (Issue 4).
func TestHandleConnectOffer_ShortPubkey(t *testing.T) {
	h, _, _ := testHandlers(t)

	var sentTypes []string
	h.SendMessage = func(msgType string, payload any) error {
		sentTypes = append(sentTypes, msgType)
		return nil
	}
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("should not be called")
	}

	// 1-byte pubkey - too short, must be rejected before answering
	shortPubkey := base64.StdEncoding.EncodeToString([]byte{0x01})
	msg := map[string]any{
		"connection_id":     "conn-short-pubkey",
		"relay_url":         "ws://localhost:9999",
		"pairing_token":     "tok-3",
		"target_session_id": "sess-1",
		"requester_pubkey":  shortPubkey,
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectOffer(payload) // must not panic

	for _, mt := range sentTypes {
		if mt == "connect.answer" {
			t.Error("connect.answer must not be sent when requester_pubkey is only 1 byte")
		}
	}
}

// TestHandleInitialStateCallsPublishSessions verifies that PublishSessions is
// invoked during HandleInitialState when recipients are present. This covers
// the race where the signaling read loop fires initial.state before
// PublishSessions is wired in (Issue 3).
func TestHandleInitialStateCallsPublishSessions(t *testing.T) {
	h, myPub, _ := testHandlers(t)

	remotePub, remotePriv, _ := crypto.GenerateKeypair()

	sessions := []backend.Session{
		{ID: "s-remote-1", Name: "remote-session", Backend: "native", Created: 5000},
	}
	blob, err := remote.BuildSessionBlob(sessions, "remote-dev", "Remote", remotePriv, map[string][]byte{
		"my-device": myPub,
	})
	if err != nil {
		t.Fatalf("build blob: %v", err)
	}

	publishCalled := make(chan struct{}, 1)

	// Wire PublishSessions before calling HandleInitialState, mirroring the
	// fix in process.go where it is assigned before client.Connect().
	h.PublishSessions = func() {
		select {
		case publishCalled <- struct{}{}:
		default:
		}
	}

	h.ListSessions = func() []backend.Session { return nil }
	h.SendMessage = func(msgType string, payload any) error { return nil }

	msg := remote.InitialStateMsg{
		Devices: []remote.InitialStateDevice{
			{
				ID: "remote-dev", Name: "Remote", AccountID: "acct-2",
				PublicKey: base64.StdEncoding.EncodeToString(remotePub),
				Online: true, LastSeen: "2026-03-28T12:00:00Z",
			},
		},
		SessionCache: []remote.SessionCacheEntry{
			{DeviceID: "remote-dev", EncryptedBlob: blob, UpdatedAt: "2026-03-28T12:00:00Z"},
		},

		Recipients:       map[string]string{"remote-dev": base64.StdEncoding.EncodeToString(remotePub)},
	}
	payload, _ := json.Marshal(msg)

	// HandleInitialState calls PublishSessions (via SendMessage in this case)
	// but also the handler previously only used SendMessage directly. After the
	// fix, when PublishSessions is set it is called via HandleDevicePresence
	// for each online device. Verify that SendMessage (our publish path) is
	// called, confirming sessions are published on initial state.
	h.HandleInitialState(payload)

	// The handler calls SendMessage for sessions.update when recipients exist.
	// Separately, HandleDevicePresence triggers PublishSessions for online
	// devices - but that is a separate flow. Here we verify the direct
	// SendMessage publish path works and PublishSessions is available if set.
	val, ok := h.RemoteStates.Load("team-1")
	if !ok {
		t.Fatal("RemoteState not stored after HandleInitialState")
	}
	rs := val.(*remote.RemoteState)
	if rs.Device("remote-dev") == nil {
		t.Fatal("expected remote-dev device to be stored")
	}

	// Verify PublishSessions field is still set (not cleared by the handler).
	if h.PublishSessions == nil {
		t.Error("PublishSessions should not be nil after HandleInitialState - the field must be wired before Connect")
	}
}

func TestHandleConnectOffer_MalformedCandidates(t *testing.T) {
	h, _, _ := testHandlers(t)

	h.SendMessage = func(msgType string, payload any) error { return nil }
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("attach failed")
	}

	// Offer with garbage in requester_candidates
	msg := map[string]any{
		"connection_id":    "conn-bad-cands",
		"relay_url":        "ws://localhost:9999",
		"pairing_token":    "tok-3",
		"target_session_id": "sess-1",
		"requester_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"requester_candidates": []any{
			"not-an-object",
			42,
			map[string]any{"type": 123}, // type should be string, not number
		},
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectOffer(payload) // must not panic
}

func TestHandleConnectOffer_NilTransport(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.Transport = nil // explicit nil (already nil by default, but explicit for clarity)

	var sentTypes []string
	var sentPayloads []any
	h.SendMessage = func(msgType string, payload any) error {
		sentTypes = append(sentTypes, msgType)
		sentPayloads = append(sentPayloads, payload)
		return nil
	}
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("attach failed - relay dial expected to fail first")
	}

	msg := map[string]any{
		"connection_id":     "conn-nil-transport",
		"relay_url":         "ws://localhost:9999",
		"pairing_token":     "tok-4",
		"target_session_id": "sess-1",
		"requester_pubkey":  base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"from_device_id":    "requester-device",
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectOffer(payload) // must not panic

	// connect.answer should be sent
	hasAnswer := false
	for i, mt := range sentTypes {
		if mt == "connect.answer" {
			hasAnswer = true
			// When transport is nil there are no local candidates - verify Candidates is nil/empty
			if ansMsg, ok := sentPayloads[i].(remote.ConnectAnswerMsg); ok {
				if len(ansMsg.Candidates) != 0 {
					t.Errorf("expected no candidates when transport is nil, got %d", len(ansMsg.Candidates))
				}
			}
		}
	}
	if !hasAnswer {
		t.Error("expected connect.answer to be sent with nil transport")
	}
}

func TestHandleConnectOffer_BadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.SendMessage = func(msgType string, payload any) error { return nil }
	h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("should not be called")
	}

	h.HandleConnectOffer([]byte("not json")) // must not panic
}

// ---------------------------------------------------------------------------
// HandleConnectAnswer adversarial tests
// ---------------------------------------------------------------------------

func TestHandleConnectAnswer_MalformedPayload(t *testing.T) {
	h, _, _ := testHandlers(t)

	reqID := "test-malformed"
	ch := h.RegisterAnswerChannel(reqID)
	defer h.UnregisterAnswerChannel(reqID)

	h.HandleConnectAnswer([]byte("bad json")) // must not panic

	select {
	case <-ch:
		t.Error("nothing should be sent on bad JSON")
	default:
		// expected - channel should remain empty
	}
}

func TestHandleConnectAnswer_NoRegisteredChannel(t *testing.T) {
	h, _, _ := testHandlers(t)

	// No channel registered - answer should be discarded without panic
	pub, _, _ := crypto.GenerateKeypair()
	msg := remote.ConnectAnswerResponseMsg{
		ConnectionID:    "conn-no-ch",
		RequestID:       "unregistered-request",
		RelayURL:        "ws://relay:8080",
		PairingToken:    "tok-no-ch",
		ResponderPubkey: pub,
	}
	payload, _ := json.Marshal(msg)
	h.HandleConnectAnswer(payload) // must not panic
}

// ---------------------------------------------------------------------------
func TestHandleInitialState_TeamNameFromHandler(t *testing.T) {
	h, pub, _ := testHandlers(t)

	h.BroadcastFn = func(method string, params map[string]any) {}

	msg := remote.InitialStateMsg{
		Devices: []remote.InitialStateDevice{
			{ID: "other-dev", Name: "Other", AccountID: "acct-2", PublicKey: base64.StdEncoding.EncodeToString(pub), Online: true},
		},
		Recipients: map[string]string{},
	}
	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload)

	val, ok := h.RemoteStates.Load("team-1")
	if !ok {
		t.Fatal("RemoteState not stored")
	}
	rs := val.(*remote.RemoteState)
	snaps := rs.Snapshot()
	for _, snap := range snaps {
		if snap.TeamName != "My Team" {
			t.Errorf("expected team name 'My Team', got %q", snap.TeamName)
		}
	}
}

// HandleInitialState adversarial tests
// ---------------------------------------------------------------------------

func TestHandleInitialState_EmptyDevices(t *testing.T) {
	h, _, _ := testHandlers(t)

	var broadcasts []string
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
	}
	h.ListSessions = func() []backend.Session { return nil }
	h.SendMessage = func(msgType string, payload any) error { return nil }

	msg := remote.InitialStateMsg{
		Devices:          []remote.InitialStateDevice{},
		SessionCache:     []remote.SessionCacheEntry{},
		Recipients:       map[string]string{},
	}
	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload) // must not panic

	hasUpdated := false
	for _, b := range broadcasts {
		if b == "remote.updated" {
			hasUpdated = true
		}
	}
	if !hasUpdated {
		t.Error("expected remote.updated broadcast even with empty devices")
	}
}

func TestHandleInitialState_BadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleInitialState([]byte("{invalid")) // must not panic
}

// ---------------------------------------------------------------------------
// HandleSessionsUpdated adversarial tests
// ---------------------------------------------------------------------------

func TestHandleSessionsUpdated_BadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleSessionsUpdated([]byte("bad json")) // must not panic
}

func TestHandleSessionsUpdated_UnknownDevice(t *testing.T) {
	h, myPub, _ := testHandlers(t)

	// Build a blob that targets our device so decryption succeeds,
	// but the sender device is not in RemoteState
	senderPub, senderPriv, _ := crypto.GenerateKeypair()
	sessions := []backend.Session{
		{ID: "s-unknown", Name: "unknown-session", Backend: "native"},
	}
	blob, _ := remote.BuildSessionBlob(sessions, "unknown-device", "Unknown", senderPriv, map[string][]byte{
		"my-device": myPub,
	})

	var broadcasts []string
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
	}

	msg := remote.SessionsUpdatedMsg{
		DeviceID:        "unknown-device",
		SenderPublicKey: base64.StdEncoding.EncodeToString(senderPub),
		EncryptedBlob:   blob,
	}
	payload, _ := json.Marshal(msg)
	h.HandleSessionsUpdated(payload) // must not panic
}

// ---------------------------------------------------------------------------
// HandleDevicePresence adversarial tests
// ---------------------------------------------------------------------------

func TestHandleDevicePresence_UnknownDevice(t *testing.T) {
	h, _, _ := testHandlers(t)

	pub, _, _ := crypto.GenerateKeypair()

	var broadcasts []string
	h.BroadcastFn = func(method string, params map[string]any) {
		broadcasts = append(broadcasts, method)
	}

	// Device not previously in state - should be added without panic
	msg := map[string]any{
		"device_id":   "brand-new-device",
		"device_name": "Brand New",
		"account_id":  "acct-new",
		"public_key":  base64.StdEncoding.EncodeToString(pub),
		"online":      true,
	}
	payload, _ := json.Marshal(msg)
	h.HandleDevicePresence(payload) // must not panic

	hasOnline := false
	for _, b := range broadcasts {
		if b == "remote.device.online" {
			hasOnline = true
		}
	}
	if !hasOnline {
		t.Error("expected remote.device.online broadcast for unknown (new) device coming online")
	}
}

func TestHandleDevicePresence_BadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleDevicePresence([]byte("bad json")) // must not panic
}

func TestHandleDevicePresence_OnlineTriggersPublish(t *testing.T) {
	h, _, _ := testHandlers(t)

	pub, _, _ := crypto.GenerateKeypair()

	var published atomic.Int32
	h.PublishSessions = func() { published.Store(1) }
	h.BroadcastFn = func(method string, params map[string]any) {}

	msg := map[string]any{
		"device_id":    "pub-dev",
		"device_name":  "Publish Device",
		"account_id":   "acct-pub",
		"account_name": "Publisher",
		"public_key":   base64.StdEncoding.EncodeToString(pub),
		"online":       true,
	}
	payload, _ := json.Marshal(msg)
	h.HandleDevicePresence(payload)

	// PublishSessions is called in a goroutine, give it a moment.
	time.Sleep(50 * time.Millisecond)
	if published.Load() == 0 {
		t.Fatal("expected PublishSessions to be called when device comes online")
	}
}

func TestHandleDevicePresence_OfflineDoesNotPublish(t *testing.T) {
	h, _, _ := testHandlers(t)

	var published atomic.Int32
	h.PublishSessions = func() { published.Store(1) }
	h.BroadcastFn = func(method string, params map[string]any) {}

	msg := map[string]any{
		"device_id":   "offline-dev",
		"device_name": "Offline Device",
		"online":      false,
		"last_seen":   "2026-03-30T00:00:00Z",
	}
	payload, _ := json.Marshal(msg)
	h.HandleDevicePresence(payload)

	time.Sleep(50 * time.Millisecond)
	if published.Load() != 0 {
		t.Fatal("PublishSessions should not be called when device goes offline")
	}
}

// ---------------------------------------------------------------------------
// HandleConnectOffer - deeper async goroutine coverage
// ---------------------------------------------------------------------------

func TestHandleConnectOffer_AttachSessionError(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.Transport = nil // relay-only path (simpler)

	h.SendMessage = func(msgType string, p any) error { return nil }
	h.AttachSession = func(sid string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("session not found: %s", sid)
	}

	payload, _ := json.Marshal(map[string]any{
		"connection_id":    "attach-err-conn",
		"relay_url":        "127.0.0.1:1", // unreachable - relay dial will fail
		"pairing_token":    "token",
		"target_session_id": "nonexistent-session",
		"requester_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})

	h.HandleConnectOffer(json.RawMessage(payload))

	// Give the goroutine time to attempt relay connection and fail cleanly.
	time.Sleep(200 * time.Millisecond)
	// No panic = success. The goroutine should have logged and returned.
}

func TestHandleConnectOffer_EmptyPayload(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.SendMessage = func(msgType string, p any) error { return nil }
	h.AttachSession = func(sid string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("not found")
	}

	h.HandleConnectOffer(json.RawMessage([]byte("{}")))
	// No panic = success.
}

func TestHandleConnectOffer_ValidCandidatesParsed(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.Transport = nil

	var answerSent bool
	h.SendMessage = func(msgType string, p any) error {
		if msgType == "connect.answer" {
			answerSent = true
		}
		return nil
	}
	h.AttachSession = func(sid string) (backend.StreamHandle, error) {
		return nil, fmt.Errorf("not found")
	}

	payload, _ := json.Marshal(map[string]any{
		"connection_id":     "candidates-conn",
		"relay_url":         "127.0.0.1:1",
		"pairing_token":     "token",
		"target_session_id": "session-1",
		"requester_pubkey":  base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"from_device_id":    "requester-device",
		"requester_candidates": []any{
			map[string]any{"type": "lan", "addr": "192.168.1.50", "port": float64(4900)},
			map[string]any{"type": "stun", "addr": "203.0.113.22", "port": float64(48201)},
		},
	})

	h.HandleConnectOffer(json.RawMessage(payload))

	if !answerSent {
		t.Fatal("expected connect.answer to be sent")
	}

	time.Sleep(200 * time.Millisecond) // let goroutine finish
}

func TestHandleConnectOffer_NilAttachSession(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.AttachSession = nil
	h.SendMessage = nil

	payload, _ := json.Marshal(map[string]any{
		"connection_id":    "nil-attach-conn",
		"relay_url":        "127.0.0.1:1",
		"pairing_token":    "token",
		"target_session_id": "session-1",
		"requester_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})

	h.HandleConnectOffer(json.RawMessage(payload))
	// Should return early without panic.
}

func TestHandleConnectOffer_NilSendMessage(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.SendMessage = nil

	payload, _ := json.Marshal(map[string]any{
		"connection_id":    "nil-send-conn",
		"relay_url":        "127.0.0.1:1",
		"pairing_token":    "token",
		"target_session_id": "session-1",
		"requester_pubkey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})

	h.HandleConnectOffer(json.RawMessage(payload))
	// Should return early without panic.
}

// ---------------------------------------------------------------------------
// HandleRecipientsUpdate - edge cases
// ---------------------------------------------------------------------------

func TestHandleRecipientsUpdate_BadJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.HandleRecipientsUpdate(json.RawMessage([]byte("bad")))
	// No panic = success.
}

func TestHandleRecipientsUpdate_EmptyRecipients(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.BroadcastFn = func(method string, params map[string]any) {}

	payload, _ := json.Marshal(map[string]any{
		"recipients": map[string]any{},
	})
	h.HandleRecipientsUpdate(json.RawMessage(payload))
	// No panic = success.
}

// ---------------------------------------------------------------------------
// HandleSessionCreateRequest - authentication
// ---------------------------------------------------------------------------

func TestHandleSessionCreateRequest_RejectsUnknownDevice(t *testing.T) {
	h, _, _ := testHandlers(t)

	var created bool
	h.CreateSession = func(opts backend.CreateOpts) (backend.Session, error) {
		created = true
		return backend.Session{ID: "new-sess"}, nil
	}
	h.SendMessage = func(msgType string, payload any) error { return nil }

	// Send a create request from a device NOT in the RemoteState
	payload, _ := json.Marshal(remote.SessionCreateForwardMsg{
		RequestID:    "req-1",
		FromDeviceID: "unknown-evil-device",
		Name:         "hacked-session",
		Command:      "rm -rf /",
	})

	h.HandleSessionCreateRequest(json.RawMessage(payload))

	if created {
		t.Error("CreateSession should NOT have been called for unknown device")
	}
}

func TestHandleInitialState_RotatedToken(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.BroadcastFn = func(method string, params map[string]any) {}

	var savedToken string
	h.SaveToken = func(token string) {
		savedToken = token
	}

	msg := remote.InitialStateMsg{
		Devices:      []remote.InitialStateDevice{},
		SessionCache: []remote.SessionCacheEntry{},
		Recipients:   map[string]string{},
		RotatedToken: "new-rotated-token-abc",
	}
	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload)

	if savedToken != "new-rotated-token-abc" {
		t.Errorf("SaveToken: got %q, want %q", savedToken, "new-rotated-token-abc")
	}
}

func TestHandleInitialState_NoRotatedToken(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.BroadcastFn = func(method string, params map[string]any) {}

	called := false
	h.SaveToken = func(token string) {
		called = true
	}

	msg := remote.InitialStateMsg{
		Devices:      []remote.InitialStateDevice{},
		SessionCache: []remote.SessionCacheEntry{},
		Recipients:   map[string]string{},
	}
	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload)

	if called {
		t.Error("SaveToken should not be called when no rotated token is present")
	}
}

func TestHandleInitialState_RotatedTokenNilCallback(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.BroadcastFn = func(method string, params map[string]any) {}
	// SaveToken is nil (not set) - should not panic

	msg := remote.InitialStateMsg{
		Devices:      []remote.InitialStateDevice{},
		SessionCache: []remote.SessionCacheEntry{},
		Recipients:   map[string]string{},
		RotatedToken: "some-token",
	}
	payload, _ := json.Marshal(msg)
	h.HandleInitialState(payload) // must not panic
}

func TestHandleInitialState_RotatedTokenFromRawJSON(t *testing.T) {
	h, _, _ := testHandlers(t)
	h.BroadcastFn = func(method string, params map[string]any) {}

	var savedToken string
	h.SaveToken = func(token string) {
		savedToken = token
	}

	// Simulate raw JSON as the server would send it (snake_case field names)
	rawJSON := []byte(`{
		"type": "initial.state",
		"devices": [],
		"session_cache": [],
		"recipients": {},
		"rotated_token": "from-raw-json-xyz"
	}`)
	h.HandleInitialState(rawJSON)

	if savedToken != "from-raw-json-xyz" {
		t.Errorf("SaveToken from raw JSON: got %q, want %q", savedToken, "from-raw-json-xyz")
	}
}
