package daemon

import (
	"context"
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/remote"
	"github.com/carryon-dev/cli/internal/session"
)

func newTestRemoteSubsystem(t *testing.T) *RemoteSubsystem {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.NewManager(tmpDir)
	logStore := logging.NewStore(tmpDir, 1)
	t.Cleanup(func() { logStore.Close() })
	logger := logging.NewLogger(logStore, "debug")
	registry := backend.NewRegistry()
	sm := session.NewManager(registry, "native")

	creds := &remote.Credentials{
		DeviceID:     "test-device",
		DeviceName:   "Test Device",
		AccountID:    "test-account",
		TeamID:       "test-team",
		TeamName:     "Test Team",
		SessionToken: "initial-token",
	}
	if err := remote.SaveCredentials(tmpDir, creds); err != nil {
		t.Fatalf("save initial credentials: %v", err)
	}

	return &RemoteSubsystem{
		creds:          creds,
		remotePath:     tmpDir,
		cfg:            cfg,
		logger:         logger,
		sessionManager: sm,
		daemonCtx:      context.Background(),
	}
}

func TestRemoteSubsystem_StatusDisconnected(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	status := rs.Status()

	connected, ok := status["connected"].(bool)
	if !ok {
		t.Fatal("expected 'connected' to be a bool")
	}
	if connected {
		t.Error("expected connected=false when no client is set")
	}

	if got := status["device_id"]; got != "test-device" {
		t.Errorf("device_id: got %q, want %q", got, "test-device")
	}
	if got := status["device_name"]; got != "Test Device" {
		t.Errorf("device_name: got %q, want %q", got, "Test Device")
	}
	if got := status["account_id"]; got != "test-account" {
		t.Errorf("account_id: got %q, want %q", got, "test-account")
	}
}

func TestRemoteSubsystem_DisconnectWhenNotConnected(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Should not panic when no client is connected.
	rs.Disconnect()
}

func TestRemoteSubsystem_ConnectRejectsDouble(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Simulate that a connect is already in progress.
	rs.mu.Lock()
	rs.connecting = true
	rs.mu.Unlock()

	err := rs.Connect()
	if err == nil {
		t.Fatal("expected error when connect is already in progress")
	}
	if err.Error() != "connect already in progress" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemoteSubsystem_ConnectWhenAlreadyConnected(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Simulate an existing client by setting a non-nil value.
	// We use an actual SignalingClient here so the type check passes,
	// but we never call Connect() on it - we just need a non-nil pointer.
	rs.mu.Lock()
	rs.client = remote.NewSignalingClient("ws://fake", "d", "n", "t", "s", nil)
	rs.mu.Unlock()

	err := rs.Connect()
	if err != nil {
		t.Fatalf("expected nil error when already connected, got: %v", err)
	}
}

func TestRemoteSubsystem_DevicesEmpty(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	devices := rs.Devices()
	if devices == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(devices) != 0 {
		t.Errorf("expected 0 devices, got %d", len(devices))
	}
}

func TestRemoteSubsystem_DevicesWithState(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Create a RemoteState with a device and sessions.
	state := remote.NewRemoteState("test-team", "Test Team")
	state.SetDevice(&remote.RemoteDevice{
		ID:          "remote-dev-1",
		Name:        "Remote Laptop",
		AccountID:   "acct-1",
		AccountName: "Alice",
		Online:      true,
		LastSeen:    "2026-04-01T00:00:00Z",
	})
	state.SetSessions("remote-dev-1", []remote.RemoteSession{
		{
			ID:           "native-abc123",
			Name:         "my-session",
			DeviceID:     "remote-dev-1",
			DeviceName:   "Remote Laptop",
			Created:      1711929600,
			LastAttached: 1711933200,
		},
	})

	rs.states.Store("test-team", state)

	devices := rs.Devices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	dev := devices[0]
	if dev["id"] != "remote-dev-1" {
		t.Errorf("device id: got %q, want %q", dev["id"], "remote-dev-1")
	}
	if dev["name"] != "Remote Laptop" {
		t.Errorf("device name: got %q, want %q", dev["name"], "Remote Laptop")
	}
	if dev["owner_name"] != "Alice" {
		t.Errorf("owner_name: got %q, want %q", dev["owner_name"], "Alice")
	}
	if dev["online"] != true {
		t.Errorf("online: got %v, want true", dev["online"])
	}
	if dev["team_id"] != "test-team" {
		t.Errorf("team_id: got %q, want %q", dev["team_id"], "test-team")
	}
	if dev["team_name"] != "Test Team" {
		t.Errorf("team_name: got %q, want %q", dev["team_name"], "Test Team")
	}

	sessions, ok := dev["sessions"].([]map[string]any)
	if !ok {
		t.Fatal("expected sessions to be []map[string]any")
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	sess := sessions[0]
	if sess["id"] != "native-abc123" {
		t.Errorf("session id: got %q, want %q", sess["id"], "native-abc123")
	}
	if sess["name"] != "my-session" {
		t.Errorf("session name: got %q, want %q", sess["name"], "my-session")
	}
	if sess["device_id"] != "remote-dev-1" {
		t.Errorf("session device_id: got %q, want %q", sess["device_id"], "remote-dev-1")
	}
	if sess["device_name"] != "Remote Laptop" {
		t.Errorf("session device_name: got %q, want %q", sess["device_name"], "Remote Laptop")
	}
	if sess["created"] != int64(1711929600) {
		t.Errorf("session created: got %v, want %d", sess["created"], 1711929600)
	}
	if sess["last_attached"] != int64(1711933200) {
		t.Errorf("session last_attached: got %v, want %d", sess["last_attached"], 1711933200)
	}
}

func TestRemoteSubsystem_CloseWithoutConnection(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Should not panic when no client or transport is set.
	rs.Close()
}

func TestRemoteSubsystem_PublishSessionsWhenDisconnected(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Should return silently (no panic) when client is nil.
	rs.PublishSessions()
}

func TestRemoteSubsystem_NewRemote(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.NewManager(tmpDir)
	logStore := logging.NewStore(tmpDir, 1)
	t.Cleanup(func() { logStore.Close() })
	logger := logging.NewLogger(logStore, "debug")
	registry := backend.NewRegistry()
	sm := session.NewManager(registry, "native")

	rs := NewRemote(RemoteOpts{
		Creds: &remote.Credentials{
			DeviceID:   "ctor-device",
			DeviceName: "Constructor Device",
			AccountID:  "ctor-account",
			TeamID:     "ctor-team",
			TeamName:   "Constructor Team",
		},
		RemotePath:     tmpDir,
		Config:         cfg,
		Logger:         logger,
		SessionManager: sm,
		DaemonCtx:      context.Background(),
	})
	defer rs.Close()

	// Verify it was created without panic.
	if rs == nil {
		t.Fatal("expected non-nil RemoteSubsystem")
	}

	// Verify Status returns disconnected.
	status := rs.Status()
	connected, ok := status["connected"].(bool)
	if !ok {
		t.Fatal("expected 'connected' to be a bool")
	}
	if connected {
		t.Error("expected connected=false for newly created subsystem")
	}

	// Verify device info is reported in status.
	if got := status["device_id"]; got != "ctor-device" {
		t.Errorf("device_id: got %q, want %q", got, "ctor-device")
	}
	if got := status["device_name"]; got != "Constructor Device" {
		t.Errorf("device_name: got %q, want %q", got, "Constructor Device")
	}

	// Verify the info was logged.
	entries := logStore.GetRecent(100)
	found := false
	for _, e := range entries {
		if e.Message == "Device 'Constructor Device' configured (account ctor-account)" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected device configuration log entry")
	}
}

func TestRemoteSubsystem_StatusFields(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	status := rs.Status()

	expectedKeys := []string{"connected", "account_id", "device_id", "device_name"}
	for _, key := range expectedKeys {
		if _, ok := status[key]; !ok {
			t.Errorf("status missing expected key %q", key)
		}
	}

	if got := status["connected"]; got != false {
		t.Errorf("connected: got %v, want false", got)
	}
	if got := status["account_id"]; got != "test-account" {
		t.Errorf("account_id: got %q, want %q", got, "test-account")
	}
	if got := status["device_id"]; got != "test-device" {
		t.Errorf("device_id: got %q, want %q", got, "test-device")
	}
	if got := status["device_name"]; got != "Test Device" {
		t.Errorf("device_name: got %q, want %q", got, "Test Device")
	}
}

func TestRemoteSubsystem_DevicesMultipleTeams(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Team 1 with one device
	state1 := remote.NewRemoteState("team-1", "Team One")
	state1.SetDevice(&remote.RemoteDevice{
		ID:          "dev-1",
		Name:        "Laptop",
		AccountID:   "acct-1",
		AccountName: "Alice",
		Online:      true,
		LastSeen:    "2026-04-01T00:00:00Z",
	})
	state1.SetSessions("dev-1", []remote.RemoteSession{
		{ID: "sess-1", Name: "shell", DeviceID: "dev-1", DeviceName: "Laptop"},
	})

	// Team 2 with a different device
	state2 := remote.NewRemoteState("team-2", "Team Two")
	state2.SetDevice(&remote.RemoteDevice{
		ID:          "dev-2",
		Name:        "Desktop",
		AccountID:   "acct-2",
		AccountName: "Bob",
		Online:      false,
		LastSeen:    "2026-04-02T00:00:00Z",
	})
	state2.SetSessions("dev-2", []remote.RemoteSession{
		{ID: "sess-2", Name: "server", DeviceID: "dev-2", DeviceName: "Desktop"},
		{ID: "sess-3", Name: "build", DeviceID: "dev-2", DeviceName: "Desktop"},
	})

	rs.states.Store("team-1", state1)
	rs.states.Store("team-2", state2)

	devices := rs.Devices()
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}

	// Build a map by device ID for order-independent assertions.
	byID := make(map[string]map[string]any)
	for _, d := range devices {
		id, _ := d["id"].(string)
		byID[id] = d
	}

	d1, ok := byID["dev-1"]
	if !ok {
		t.Fatal("expected device dev-1 in results")
	}
	if d1["team_id"] != "team-1" {
		t.Errorf("dev-1 team_id: got %q, want %q", d1["team_id"], "team-1")
	}
	if d1["team_name"] != "Team One" {
		t.Errorf("dev-1 team_name: got %q, want %q", d1["team_name"], "Team One")
	}
	sess1, ok := d1["sessions"].([]map[string]any)
	if !ok {
		t.Fatal("expected sessions to be []map[string]any for dev-1")
	}
	if len(sess1) != 1 {
		t.Errorf("dev-1 sessions: got %d, want 1", len(sess1))
	}

	d2, ok := byID["dev-2"]
	if !ok {
		t.Fatal("expected device dev-2 in results")
	}
	if d2["team_id"] != "team-2" {
		t.Errorf("dev-2 team_id: got %q, want %q", d2["team_id"], "team-2")
	}
	sess2, ok := d2["sessions"].([]map[string]any)
	if !ok {
		t.Fatal("expected sessions to be []map[string]any for dev-2")
	}
	if len(sess2) != 2 {
		t.Errorf("dev-2 sessions: got %d, want 2", len(sess2))
	}
}

func TestRemoteSubsystem_PublishSessionsNoRecipients(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Store a state with a device but no recipients.
	state := remote.NewRemoteState("test-team", "Test Team")
	state.SetDevice(&remote.RemoteDevice{
		ID:          "remote-dev-1",
		Name:        "Remote Laptop",
		AccountID:   "acct-1",
		AccountName: "Alice",
		Online:      true,
	})
	rs.states.Store("test-team", state)

	// PublishSessions should not panic even with state present but no recipients.
	rs.PublishSessions()
}

func TestRemoteSubsystem_SaveTokenWired(t *testing.T) {
	rs := newTestRemoteSubsystem(t)

	// Simulate what Connect() does: create handlers and wire SaveToken
	h := &SignalingHandlers{
		Logger:       rs.logger,
		DeviceID:     rs.creds.DeviceID,
		RemoteStates: &rs.states,
	}

	// Wire SaveToken the same way Connect does
	h.SaveToken = func(newToken string) {
		rs.mu.Lock()
		rs.creds.SessionToken = newToken
		rs.mu.Unlock()
		_ = remote.SaveCredentials(rs.remotePath, rs.creds)
	}

	// Call SaveToken and verify creds updated
	h.SaveToken("rotated-token-xyz")

	if rs.creds.SessionToken != "rotated-token-xyz" {
		t.Errorf("SessionToken: got %q, want %q", rs.creds.SessionToken, "rotated-token-xyz")
	}

	// Verify persisted to disk
	loaded, err := remote.LoadCredentials(rs.remotePath)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	if loaded.SessionToken != "rotated-token-xyz" {
		t.Errorf("persisted SessionToken: got %q, want %q", loaded.SessionToken, "rotated-token-xyz")
	}
}
