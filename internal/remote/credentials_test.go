package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	creds := &Credentials{
		AccountID:   "acc-123",
		DeviceID:    "dev-456",
		SessionToken: "tok-abc",
		DeviceName:  "my-laptop",
		TeamID:      "team-1",
	}

	if err := SaveCredentials(dir, creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	// Verify the file exists with 0600 permissions.
	path := filepath.Join(dir, tokenFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file permissions 0600, got %04o", perm)
	}

	loaded, err := LoadCredentials(dir)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}

	if loaded.AccountID != creds.AccountID {
		t.Errorf("AccountID: got %q, want %q", loaded.AccountID, creds.AccountID)
	}
	if loaded.DeviceID != creds.DeviceID {
		t.Errorf("DeviceID: got %q, want %q", loaded.DeviceID, creds.DeviceID)
	}
	if loaded.SessionToken != creds.SessionToken {
		t.Errorf("SessionToken: got %q, want %q", loaded.SessionToken, creds.SessionToken)
	}
	if loaded.DeviceName != creds.DeviceName {
		t.Errorf("DeviceName: got %q, want %q", loaded.DeviceName, creds.DeviceName)
	}
	if loaded.TeamID != creds.TeamID {
		t.Errorf("TeamID: got %q, want %q", loaded.TeamID, creds.TeamID)
	}
}

func TestCredentialsNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadCredentials(dir)
	if err == nil {
		t.Fatal("expected error loading from empty dir, got nil")
	}
}

func TestDeleteCredentials(t *testing.T) {
	dir := t.TempDir()

	creds := &Credentials{
		AccountID:   "acc-123",
		DeviceID:    "dev-456",
		SessionToken: "tok-abc",
		DeviceName:  "my-laptop",
		TeamID:      "team-1",
	}

	if err := SaveCredentials(dir, creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	if err := DeleteCredentials(dir); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}

	_, err := LoadCredentials(dir)
	if err == nil {
		t.Fatal("expected error loading after delete, got nil")
	}
}

func TestCredentialsSaveToNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	creds := &Credentials{
		AccountID:   "acc-nested",
		DeviceID:    "dev-nested",
		SessionToken: "tok-nested",
		DeviceName:  "nested-laptop",
		TeamID:      "team-nested",
	}
	err := SaveCredentials(dir, creds)
	if err != nil {
		t.Fatalf("SaveCredentials to nested dir: %v", err)
	}

	loaded, err := LoadCredentials(dir)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if loaded.DeviceID != "dev-nested" {
		t.Fatalf("expected 'dev-nested', got %q", loaded.DeviceID)
	}
	if loaded.TeamID != "team-nested" {
		t.Fatalf("expected 'team-nested', got %q", loaded.TeamID)
	}
}
