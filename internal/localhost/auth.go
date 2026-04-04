package localhost

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AuthManager handles password verification and session token management
// for the localhost web server when exposed on the network.
type AuthManager struct {
	mu           sync.RWMutex
	passwordHash string
	sessions     map[string]time.Time // token -> expiry
}

// NewAuthManager creates a new AuthManager with no password and no sessions.
func NewAuthManager() *AuthManager {
	return &AuthManager{
		sessions: make(map[string]time.Time),
	}
}

// SetPasswordHash sets the bcrypt hash used for password verification.
// Pass "" to clear the hash and disable password auth.
func (a *AuthManager) SetPasswordHash(hash string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.passwordHash = hash
}

// PasswordHash returns the current bcrypt hash.
func (a *AuthManager) PasswordHash() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.passwordHash
}

// HasPassword returns true if a password hash is set.
func (a *AuthManager) HasPassword() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.passwordHash != ""
}

// RequiresAuth returns true when expose is true, regardless of whether a
// password is set. When exposed without a password, the HTTP handler should
// return an error rather than allowing unauthenticated access.
func (a *AuthManager) RequiresAuth(expose bool) bool {
	return expose
}

// CheckPassword verifies a plaintext password against the stored bcrypt hash.
// Returns false if no hash is set or the password does not match.
func (a *AuthManager) CheckPassword(password string) bool {
	a.mu.RLock()
	hash := a.passwordHash
	a.mu.RUnlock()

	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// CreateSession generates a random 32-byte hex session token, stores it with
// the given TTL, and returns the token string. Expired sessions are lazily
// evicted on each call to prevent unbounded map growth.
func (a *AuthManager) CreateSession(ttl time.Duration) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	token := hex.EncodeToString(b)

	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	for k, expiry := range a.sessions {
		if now.After(expiry) {
			delete(a.sessions, k)
		}
	}

	a.sessions[token] = now.Add(ttl)
	return token
}

// ValidateSession checks whether the token exists and has not expired.
// Iterates all sessions with constant-time comparison to avoid timing
// side-channels on the token value.
func (a *AuthManager) ValidateSession(token string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := time.Now()
	tokenBytes := []byte(token)
	valid := false
	for storedToken, expiry := range a.sessions {
		if subtle.ConstantTimeCompare([]byte(storedToken), tokenBytes) == 1 && now.Before(expiry) {
			valid = true
		}
	}
	return valid
}

// ClearSessions removes all stored session tokens.
func (a *AuthManager) ClearSessions() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = make(map[string]time.Time)
}
