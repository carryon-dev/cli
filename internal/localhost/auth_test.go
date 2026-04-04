package localhost

import (
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func hashPassword(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	return string(h)
}

func TestAuthManagerCheckPasswordValid(t *testing.T) {
	am := NewAuthManager()
	am.SetPasswordHash(hashPassword(t, "correct-password"))

	if !am.CheckPassword("correct-password") {
		t.Fatal("expected CheckPassword to return true for correct password")
	}
}

func TestAuthManagerCheckPasswordInvalid(t *testing.T) {
	am := NewAuthManager()
	am.SetPasswordHash(hashPassword(t, "correct-password"))

	if am.CheckPassword("wrong-password") {
		t.Fatal("expected CheckPassword to return false for wrong password")
	}
}

func TestAuthManagerCheckPasswordNoHash(t *testing.T) {
	am := NewAuthManager()

	if am.CheckPassword("anything") {
		t.Fatal("expected CheckPassword to return false when no hash is set")
	}
}

func TestAuthManagerSessionTokenRoundTrip(t *testing.T) {
	am := NewAuthManager()
	token := am.CreateSession(5 * time.Minute)

	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if len(token) != 64 { // 32 bytes = 64 hex chars
		t.Fatalf("expected 64-char hex token, got %d chars", len(token))
	}
	if !am.ValidateSession(token) {
		t.Fatal("expected newly created session to be valid")
	}
}

func TestAuthManagerSessionTokenInvalid(t *testing.T) {
	am := NewAuthManager()

	if am.ValidateSession("bogus-token-that-does-not-exist") {
		t.Fatal("expected ValidateSession to return false for bogus token")
	}
}

func TestAuthManagerSessionTokenExpired(t *testing.T) {
	am := NewAuthManager()
	token := am.CreateSession(-1 * time.Second)

	if am.ValidateSession(token) {
		t.Fatal("expected ValidateSession to return false for expired token")
	}
}

func TestAuthManagerClearSessions(t *testing.T) {
	am := NewAuthManager()
	token := am.CreateSession(5 * time.Minute)

	if !am.ValidateSession(token) {
		t.Fatal("expected session to be valid before clearing")
	}

	am.ClearSessions()

	if am.ValidateSession(token) {
		t.Fatal("expected session to be invalid after clearing")
	}
}

func TestAuthManagerRequiresAuth(t *testing.T) {
	am := NewAuthManager()

	// No password, expose=false -> false
	if am.RequiresAuth(false) {
		t.Fatal("expected RequiresAuth(false) to be false with no password")
	}

	// No password, expose=true -> true (SEC-003: always require auth when exposed)
	if !am.RequiresAuth(true) {
		t.Fatal("expected RequiresAuth(true) to be true even with no password")
	}

	am.SetPasswordHash(hashPassword(t, "secret"))

	// Has password, expose=false -> false
	if am.RequiresAuth(false) {
		t.Fatal("expected RequiresAuth(false) to be false even with password set")
	}

	// Has password, expose=true -> true
	if !am.RequiresAuth(true) {
		t.Fatal("expected RequiresAuth(true) to be true with password set")
	}
}
