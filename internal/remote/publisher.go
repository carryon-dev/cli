package remote

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/crypto"
)

// sessionBlobEnvelope wraps sessions with a timestamp so receivers can
// detect replayed blobs from a compromised signaling server.
type sessionBlobEnvelope struct {
	Timestamp int64           `json:"ts"`
	Sessions  []RemoteSession `json:"sessions"`
}

// BuildSessionBlob encrypts a list of local sessions for the given recipients
// using the sender-key model. Returns a base64-encoded SenderKeyBundle JSON string.
// Returns empty string if there are no recipients.
func BuildSessionBlob(
	sessions []backend.Session,
	deviceID string,
	deviceName string,
	senderPriv []byte,
	recipients map[string][]byte,
) (string, error) {
	if len(recipients) == 0 {
		return "", nil
	}

	// Convert backend sessions to remote sessions
	remoteSessions := make([]RemoteSession, len(sessions))
	for i, s := range sessions {
		remoteSessions[i] = RemoteSession{
			ID:           s.ID,
			Name:         s.Name,
			DeviceID:     deviceID,
			DeviceName:   deviceName,
			Created:      s.Created,
			LastAttached: s.LastAttached,
		}
	}

	envelope := sessionBlobEnvelope{
		Timestamp: time.Now().UnixMilli(),
		Sessions:  remoteSessions,
	}
	plaintext, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal sessions: %w", err)
	}

	bundleJSON, err := crypto.SenderKeyEncrypt(plaintext, senderPriv, recipients)
	if err != nil {
		return "", fmt.Errorf("encrypt sessions: %w", err)
	}

	return base64.StdEncoding.EncodeToString(bundleJSON), nil
}

// DecryptSessionBlob decrypts an incoming session blob from another device.
// Returns the decoded remote sessions and the blob timestamp (milliseconds).
// The caller should check the timestamp against the last seen value per device
// to reject replayed blobs.
func DecryptSessionBlob(
	encryptedBlob string,
	myDeviceID string,
	myPrivateKey []byte,
	senderPublicKey []byte,
) ([]RemoteSession, int64, error) {
	bundleJSON, err := base64.StdEncoding.DecodeString(encryptedBlob)
	if err != nil {
		return nil, 0, fmt.Errorf("base64 decode: %w", err)
	}

	plaintext, err := crypto.SenderKeyDecrypt(bundleJSON, myDeviceID, myPrivateKey, senderPublicKey)
	if err != nil {
		return nil, 0, fmt.Errorf("decrypt: %w", err)
	}

	var envelope sessionBlobEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return nil, 0, fmt.Errorf("unmarshal sessions: %w", err)
	}
	if envelope.Timestamp == 0 {
		return nil, 0, fmt.Errorf("session blob missing timestamp")
	}

	return envelope.Sessions, envelope.Timestamp, nil
}
