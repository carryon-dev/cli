package cmd

import (
	"testing"

	"github.com/carryon-dev/cli/internal/backend"
)

func TestStringVal(t *testing.T) {
	m := map[string]any{
		"name":  "hello",
		"count": 42,
	}
	if got := stringVal(m, "name"); got != "hello" {
		t.Errorf("expected hello, got %s", got)
	}
	if got := stringVal(m, "missing"); got != "" {
		t.Errorf("expected empty, got %s", got)
	}
	if got := stringVal(m, "count"); got != "" {
		t.Errorf("expected empty for non-string, got %s", got)
	}
}

func TestToSessionList(t *testing.T) {
	input := []any{
		map[string]any{
			"id":              "native-abc",
			"name":            "my-project",
			"backend":         "native",
			"created":         float64(1000),
			"attachedClients": float64(2),
			"pid":             float64(12345),
			"lastAttached":    float64(2000),
			"cwd":             "/home/user/dev",
			"command":         "bash",
		},
	}
	sessions := toSessionList(input)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.ID != "native-abc" {
		t.Errorf("ID: expected native-abc, got %s", s.ID)
	}
	if s.Name != "my-project" {
		t.Errorf("Name: expected my-project, got %s", s.Name)
	}
	if s.Backend != "native" {
		t.Errorf("Backend: expected native, got %s", s.Backend)
	}
	if s.Created != 1000 {
		t.Errorf("Created: expected 1000, got %d", s.Created)
	}
	if s.AttachedClients != 2 {
		t.Errorf("AttachedClients: expected 2, got %d", s.AttachedClients)
	}
}

func TestToSessionListEmpty(t *testing.T) {
	sessions := toSessionList(nil)
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
	sessions = toSessionList("not a list")
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions for non-list, got %d", len(sessions))
	}
}

func TestParseRemoteDeviceSessions(t *testing.T) {
	// Simulate what the list command does when parsing remote.devices response
	deviceData := map[string]any{
		"id":         "dev-123",
		"name":       "MacBook Pro",
		"online":     true,
		"owner_name": "Alice",
		"sessions": []any{
			map[string]any{
				"id":            "native-1",
				"name":          "api-work",
				"created":       float64(5000),
				"last_attached": float64(6000),
			},
			map[string]any{
				"id":      "native-2",
				"name":    "deploy",
				"created": float64(7000),
			},
		},
	}

	var devSessions []backend.Session
	if rawSessions, ok := deviceData["sessions"].([]any); ok {
		for _, rs := range rawSessions {
			rsm, _ := rs.(map[string]any)
			sess := backend.Session{
				ID:   stringVal(rsm, "id"),
				Name: stringVal(rsm, "name"),
			}
			if created, ok := rsm["created"].(float64); ok {
				sess.Created = int64(created)
			}
			if la, ok := rsm["last_attached"].(float64); ok {
				sess.LastAttached = int64(la)
			}
			devSessions = append(devSessions, sess)
		}
	}

	if len(devSessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(devSessions))
	}
	if devSessions[0].Name != "api-work" {
		t.Errorf("expected api-work, got %s", devSessions[0].Name)
	}
	if devSessions[0].Created != 5000 {
		t.Errorf("expected 5000, got %d", devSessions[0].Created)
	}
	if devSessions[1].LastAttached != 0 {
		t.Errorf("expected 0 (not set), got %d", devSessions[1].LastAttached)
	}

	ownerName := stringVal(deviceData, "owner_name")
	if ownerName != "Alice" {
		t.Errorf("expected Alice, got %s", ownerName)
	}
}
