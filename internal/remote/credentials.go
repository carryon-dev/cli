package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const tokenFile = "token.json"

// Credentials holds the device auth tokens for the remote relay service.
type Credentials struct {
	AccountID    string `json:"account_id"`
	DeviceID     string `json:"device_id"`
	SessionToken string `json:"session_token"`
	DeviceName   string `json:"device_name"`
	TeamID       string `json:"team_id"`
	TeamName     string `json:"team_name,omitempty"`
}

// SaveCredentials writes credentials to dir/token.json with 0600 permissions.
// The directory is created if it does not exist.
func SaveCredentials(dir string, creds *Credentials) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	path := filepath.Join(dir, tokenFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write credentials file: %w", err)
	}

	return nil
}

// LoadCredentials reads credentials from dir/token.json.
// Returns an error if the file does not exist or cannot be parsed.
func LoadCredentials(dir string) (*Credentials, error) {
	path := filepath.Join(dir, tokenFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no credentials found at %s", path)
		}
		return nil, fmt.Errorf("read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials file: %w", err)
	}

	return &creds, nil
}

// DeleteCredentials removes the credentials directory and all its contents.
func DeleteCredentials(dir string) error {
	return os.RemoveAll(dir)
}
