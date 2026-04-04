package daemon

import "testing"

func TestIsDevMode(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"dev string", "dev", true},
		{"empty string", "", true},
		{"prod version", "1.2.3", false},
		{"semver with v prefix", "v1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.version
			got := IsDevMode()
			if got != tt.want {
				t.Errorf("IsDevMode() with version %q = %v, want %v", tt.version, got, tt.want)
			}
		})
	}

	// Restore to dev after tests
	version = "dev"
}

func TestSignalingURL(t *testing.T) {
	t.Run("dev mode returns localhost", func(t *testing.T) {
		version = "dev"
		got := SignalingURL()
		want := "ws://localhost:8787/ws/connect"
		if got != want {
			t.Errorf("SignalingURL() in dev mode = %q, want %q", got, want)
		}
	})

	t.Run("empty version returns localhost", func(t *testing.T) {
		version = ""
		got := SignalingURL()
		want := "ws://localhost:8787/ws/connect"
		if got != want {
			t.Errorf("SignalingURL() with empty version = %q, want %q", got, want)
		}
	})

	t.Run("prod version returns production URL", func(t *testing.T) {
		version = "1.2.3"
		got := SignalingURL()
		want := "wss://carryon.dev/ws/connect"
		if got != want {
			t.Errorf("SignalingURL() in prod mode = %q, want %q", got, want)
		}
	})

	// Restore to dev after tests
	version = "dev"
}

func TestAPIBaseURL(t *testing.T) {
	t.Run("dev mode returns localhost", func(t *testing.T) {
		version = "dev"
		got := APIBaseURL()
		want := "http://localhost:8787"
		if got != want {
			t.Errorf("APIBaseURL() in dev mode = %q, want %q", got, want)
		}
	})

	t.Run("empty version returns localhost", func(t *testing.T) {
		version = ""
		got := APIBaseURL()
		want := "http://localhost:8787"
		if got != want {
			t.Errorf("APIBaseURL() with empty version = %q, want %q", got, want)
		}
	})

	t.Run("prod version returns production URL", func(t *testing.T) {
		version = "1.2.3"
		got := APIBaseURL()
		want := "https://carryon.dev"
		if got != want {
			t.Errorf("APIBaseURL() in prod mode = %q, want %q", got, want)
		}
	})

	// Restore to dev after tests
	version = "dev"
}

func TestSetVersion(t *testing.T) {
	original := version
	defer func() { version = original }()

	SetVersion("1.0.0")
	if version != "1.0.0" {
		t.Errorf("SetVersion() did not set version, got %q", version)
	}

	SetVersion("dev")
	if version != "dev" {
		t.Errorf("SetVersion() did not set version to dev, got %q", version)
	}
}
