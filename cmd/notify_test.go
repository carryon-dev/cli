package cmd

import (
	"testing"
	"time"
)

func TestNotifyDaemonNoHang(t *testing.T) {
	// Use a socket path that doesn't exist.
	start := time.Now()
	notifyDaemon("/tmp/nonexistent-carryon-test.sock", "session.detached", map[string]any{
		"sessionId": "fake-session",
	})
	elapsed := time.Since(start)

	// Should return within 600ms (500ms timeout + margin).
	if elapsed > 600*time.Millisecond {
		t.Fatalf("notifyDaemon took %v, expected < 600ms", elapsed)
	}
}
