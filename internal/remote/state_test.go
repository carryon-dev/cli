package remote

import (
	"testing"
)

func TestRemoteStateDevices(t *testing.T) {
	rs := NewRemoteState("team-1", "Team One")

	rs.SetDevice(&RemoteDevice{
		ID:        "dev-1",
		Name:      "MacBook Pro",
		AccountID: "acct-1",
		PublicKey: []byte("pubkey-1"),
		Online:    true,
		LastSeen:  "2026-03-28T12:00:00Z",
	})

	devices := rs.Devices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].Name != "MacBook Pro" {
		t.Fatalf("expected MacBook Pro, got %s", devices[0].Name)
	}
}

func TestRemoteStateSetDeviceOnline(t *testing.T) {
	rs := NewRemoteState("team-1", "Team One")
	rs.SetDevice(&RemoteDevice{
		ID: "dev-1", Name: "Laptop", Online: true,
	})

	rs.SetDeviceOffline("dev-1", "2026-03-28T13:00:00Z")
	dev := rs.Device("dev-1")
	if dev == nil {
		t.Fatal("device should exist")
	}
	if dev.Online {
		t.Fatal("device should be offline")
	}
	if dev.LastSeen != "2026-03-28T13:00:00Z" {
		t.Fatalf("expected updated last_seen, got %s", dev.LastSeen)
	}

	// Set back online - updates existing device.
	rs.SetDeviceOnline("dev-1", "Laptop Pro", "acct-1", "My Account", []byte("pubkey"))
	dev = rs.Device("dev-1")
	if dev == nil {
		t.Fatal("device should still exist")
	}
	if !dev.Online {
		t.Fatal("expected device to be online")
	}
	if dev.Name != "Laptop Pro" {
		t.Fatalf("expected updated name 'Laptop Pro', got %q", dev.Name)
	}
	if dev.AccountName != "My Account" {
		t.Fatalf("expected 'My Account', got %q", dev.AccountName)
	}

	// Set online for a device that does not exist yet - should create it.
	rs.SetDeviceOnline("new-dev", "New Device", "acct-2", "Other Account", []byte("pubkey2"))
	dev = rs.Device("new-dev")
	if dev == nil {
		t.Fatal("expected new device to be created")
	}
	if !dev.Online {
		t.Fatal("expected new device to be online")
	}
	if dev.Name != "New Device" {
		t.Fatalf("expected 'New Device', got %q", dev.Name)
	}
}

func TestRemoteStateSessions(t *testing.T) {
	rs := NewRemoteState("team-1", "Team One")

	sessions := []RemoteSession{
		{ID: "s1", Name: "project-a", DeviceID: "dev-1", DeviceName: "Laptop", Created: 1000},
		{ID: "s2", Name: "project-b", DeviceID: "dev-1", DeviceName: "Laptop", Created: 2000},
	}

	rs.SetSessions("dev-1", sessions)

	got := rs.Sessions("dev-1")
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	if got[0].Name != "project-a" {
		t.Fatalf("expected project-a, got %s", got[0].Name)
	}
}

func TestRemoteStateRecipients(t *testing.T) {
	rs := NewRemoteState("team-1", "Team One")
	rs.SetRecipients(map[string][]byte{
		"dev-2": []byte("pubkey-2"),
		"dev-3": []byte("pubkey-3"),
	})

	recipients := rs.Recipients()
	if len(recipients) != 2 {
		t.Fatalf("expected 2 recipients, got %d", len(recipients))
	}
}

func TestRemoteStateSnapshot(t *testing.T) {
	rs := NewRemoteState("team-1", "Team One")
	rs.SetDevice(&RemoteDevice{
		ID: "dev-1", Name: "Laptop", Online: true,
	})
	rs.SetSessions("dev-1", []RemoteSession{
		{ID: "s1", Name: "shell", DeviceID: "dev-1", DeviceName: "Laptop"},
	})

	snap := rs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 device snapshot, got %d", len(snap))
	}
	if snap[0].DeviceName != "Laptop" {
		t.Fatalf("expected Laptop, got %s", snap[0].DeviceName)
	}
	if len(snap[0].Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap[0].Sessions))
	}
}
