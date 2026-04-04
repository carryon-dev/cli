package cmd

import (
	"testing"
)

func TestMatchDeviceExactName(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "MacBook Pro", "online": true},
		{"id": "dev-2", "name": "Build Server", "online": true},
	}
	matched, err := matchDevice("MacBook Pro", devices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched["id"] != "dev-1" {
		t.Errorf("expected dev-1, got %v", matched["id"])
	}
}

func TestMatchDeviceExactID(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "MacBook Pro", "online": true},
	}
	matched, err := matchDevice("dev-1", devices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched["name"] != "MacBook Pro" {
		t.Errorf("expected MacBook Pro, got %v", matched["name"])
	}
}

func TestMatchDevicePrefixName(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "MacBook Pro", "online": true},
		{"id": "dev-2", "name": "Build Server", "online": true},
	}
	matched, err := matchDevice("Mac", devices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched["id"] != "dev-1" {
		t.Errorf("expected dev-1, got %v", matched["id"])
	}
}

func TestMatchDevicePrefixID(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-abc-123", "name": "Laptop", "online": true},
	}
	matched, err := matchDevice("dev-abc", devices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched["name"] != "Laptop" {
		t.Errorf("expected Laptop, got %v", matched["name"])
	}
}

func TestMatchDeviceNoMatch(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "MacBook Pro", "online": true},
	}
	_, err := matchDevice("nonexistent", devices)
	if err == nil {
		t.Fatal("expected error for no match")
	}
}

func TestMatchDeviceMultipleMatches(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "MacBook Pro 14", "online": true},
		{"id": "dev-2", "name": "MacBook Pro 16", "online": true},
	}
	_, err := matchDevice("MacBook", devices)
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
}

func TestMatchDeviceEmptyList(t *testing.T) {
	_, err := matchDevice("anything", nil)
	if err == nil {
		t.Fatal("expected error for empty list")
	}
}

func TestMatchDeviceExactTakesPriorityOverPrefix(t *testing.T) {
	devices := []map[string]any{
		{"id": "dev-1", "name": "Mac", "online": true},
		{"id": "dev-2", "name": "MacBook Pro", "online": true},
	}
	matched, err := matchDevice("Mac", devices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exact name match on "Mac" should win over prefix match on "MacBook Pro"
	if matched["id"] != "dev-1" {
		t.Errorf("expected dev-1 (exact match), got %v", matched["id"])
	}
}
