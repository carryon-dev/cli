package cmd

import (
	"testing"
	"time"
)

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		created  int64
		expected string
	}{
		{"just now", now.Add(-5 * time.Second).UnixMilli(), "just now"},
		{"seconds ago", now.Add(-30 * time.Second).UnixMilli(), "just now"},
		{"minutes ago", now.Add(-5 * time.Minute).UnixMilli(), "5m ago"},
		{"one minute ago", now.Add(-90 * time.Second).UnixMilli(), "1m ago"},
		{"hours ago", now.Add(-2 * time.Hour).UnixMilli(), "2h ago"},
		{"yesterday", now.Add(-30 * time.Hour).UnixMilli(), "yesterday"},
		{"days ago", now.Add(-3 * 24 * time.Hour).UnixMilli(), "3d ago"},
		{"weeks ago", now.Add(-14 * 24 * time.Hour).UnixMilli(), "2w ago"},
		{"old date fallback", time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local).UnixMilli(), "2026-01-15"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeTime(tt.created)
			if got != tt.expected {
				t.Errorf("relativeTime(%d) = %q, want %q", tt.created, got, tt.expected)
			}
		})
	}
}
