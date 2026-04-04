package remote

import (
	"sync"
)

// RemoteDevice represents a device visible in a team.
type RemoteDevice struct {
	ID          string
	Name        string
	AccountID   string
	AccountName string
	PublicKey   []byte
	Online      bool
	LastSeen    string
}

// DeviceSnapshot is a device with its sessions, used for RPC responses.
type DeviceSnapshot struct {
	DeviceID    string          `json:"id"`
	DeviceName  string          `json:"name"`
	AccountID   string          `json:"account_id"`
	AccountName string          `json:"account_name"`
	Online      bool            `json:"online"`
	LastSeen    string          `json:"last_seen"`
	TeamID      string          `json:"team_id"`
	TeamName    string          `json:"team_name"`
	Sessions    []RemoteSession `json:"sessions"`
}

// RemoteState is a thread-safe in-memory store for remote devices and sessions
// within a single team.
type RemoteState struct {
	mu             sync.RWMutex
	devices        map[string]*RemoteDevice
	sessions       map[string][]RemoteSession
	recipients     map[string][]byte // deviceId -> public key
	blobTimestamps map[string]int64  // deviceId -> last seen blob timestamp (ms)
	teamID         string
	teamName       string
}

// NewRemoteState creates a new RemoteState for the given team.
func NewRemoteState(teamID, teamName string) *RemoteState {
	return &RemoteState{
		devices:        make(map[string]*RemoteDevice),
		sessions:       make(map[string][]RemoteSession),
		recipients:     make(map[string][]byte),
		blobTimestamps: make(map[string]int64),
		teamID:         teamID,
		teamName:       teamName,
	}
}

// CheckBlobTimestamp returns true if ts is newer than the last seen timestamp
// for this device, and updates the stored value. Returns false (replay) if
// ts <= the last seen value.
func (rs *RemoteState) CheckBlobTimestamp(deviceID string, ts int64) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if ts <= rs.blobTimestamps[deviceID] {
		return false
	}
	rs.blobTimestamps[deviceID] = ts
	return true
}

// SetDevice adds or updates a device in the store.
func (rs *RemoteState) SetDevice(dev *RemoteDevice) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.devices[dev.ID] = dev
}

// Device returns a device by ID, or nil if not found.
func (rs *RemoteState) Device(id string) *RemoteDevice {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	dev := rs.devices[id]
	if dev == nil {
		return nil
	}
	copy := *dev
	return &copy
}

// Devices returns a copy of all devices.
func (rs *RemoteState) Devices() []RemoteDevice {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	result := make([]RemoteDevice, 0, len(rs.devices))
	for _, d := range rs.devices {
		result = append(result, *d)
	}
	return result
}

// SetDeviceOnline marks a device as online.
func (rs *RemoteState) SetDeviceOnline(id, name, accountID, accountName string, publicKey []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if dev, ok := rs.devices[id]; ok {
		dev.Online = true
		dev.Name = name
		dev.AccountName = accountName
		dev.PublicKey = publicKey
	} else {
		rs.devices[id] = &RemoteDevice{
			ID: id, Name: name, AccountID: accountID, AccountName: accountName,
			PublicKey: publicKey, Online: true,
		}
	}
}

// SetDeviceOnlineStatus marks a device as online and updates metadata but
// preserves the existing PublicKey. Use this when the device already has an
// established key (from initial.state) and we do not want a presence event
// to overwrite it.
func (rs *RemoteState) SetDeviceOnlineStatus(id, name, accountID, accountName string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if dev, ok := rs.devices[id]; ok {
		dev.Online = true
		dev.Name = name
		dev.AccountName = accountName
	} else {
		rs.devices[id] = &RemoteDevice{
			ID: id, Name: name, AccountID: accountID, AccountName: accountName,
			Online: true,
		}
	}
}

// SetDeviceOffline marks a device as offline.
func (rs *RemoteState) SetDeviceOffline(id, lastSeen string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if dev, ok := rs.devices[id]; ok {
		dev.Online = false
		dev.LastSeen = lastSeen
	}
}

// SetSessions replaces the session list for a device.
func (rs *RemoteState) SetSessions(deviceID string, sessions []RemoteSession) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.sessions[deviceID] = sessions
}

// Sessions returns the session list for a device.
func (rs *RemoteState) Sessions(deviceID string) []RemoteSession {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	s := rs.sessions[deviceID]
	result := make([]RemoteSession, len(s))
	copy(result, s)
	return result
}

// SetRecipients replaces the recipient public key map.
func (rs *RemoteState) SetRecipients(recipients map[string][]byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.recipients = recipients
}

// Recipients returns a copy of the recipient map.
func (rs *RemoteState) Recipients() map[string][]byte {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	result := make(map[string][]byte, len(rs.recipients))
	for k, v := range rs.recipients {
		result[k] = v
	}
	return result
}

// Snapshot returns all devices with their sessions, suitable for RPC responses.
func (rs *RemoteState) Snapshot() []DeviceSnapshot {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	result := make([]DeviceSnapshot, 0, len(rs.devices))
	for _, dev := range rs.devices {
		sessions := rs.sessions[dev.ID]
		sessionsCopy := make([]RemoteSession, len(sessions))
		copy(sessionsCopy, sessions)
		result = append(result, DeviceSnapshot{
			DeviceID:    dev.ID,
			DeviceName:  dev.Name,
			AccountID:   dev.AccountID,
			AccountName: dev.AccountName,
			Online:      dev.Online,
			LastSeen:    dev.LastSeen,
			TeamID:      rs.teamID,
			TeamName:    rs.teamName,
			Sessions:    sessionsCopy,
		})
	}
	return result
}
