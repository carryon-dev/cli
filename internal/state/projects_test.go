package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAssociations_AssociateAndGet(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/home/user/myproject", "sess-1")

	assoc := pa.GetForProject("/home/user/myproject")
	if assoc.Path != "/home/user/myproject" {
		t.Fatalf("expected path '/home/user/myproject', got %q", assoc.Path)
	}
	if len(assoc.Associated) != 1 {
		t.Fatalf("expected 1 association, got %d", len(assoc.Associated))
	}
	if assoc.Associated[0].SessionID != "sess-1" {
		t.Fatalf("expected sessionId 'sess-1', got %q", assoc.Associated[0].SessionID)
	}
	if assoc.Associated[0].AddedAt == "" {
		t.Fatal("expected addedAt to be non-empty")
	}
}

func TestProjectAssociations_Idempotent(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/home/user/proj", "sess-1")
	pa.Associate("/home/user/proj", "sess-1")

	assoc := pa.GetForProject("/home/user/proj")
	if len(assoc.Associated) != 1 {
		t.Fatalf("expected 1 association (idempotent), got %d", len(assoc.Associated))
	}
}

func TestProjectAssociations_Disassociate(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/proj", "sess-1")
	pa.Associate("/proj", "sess-2")
	pa.Disassociate("/proj", "sess-1")

	assoc := pa.GetForProject("/proj")
	if len(assoc.Associated) != 1 {
		t.Fatalf("expected 1 association after disassociate, got %d", len(assoc.Associated))
	}
	if assoc.Associated[0].SessionID != "sess-2" {
		t.Fatalf("expected remaining session 'sess-2', got %q", assoc.Associated[0].SessionID)
	}
}

func TestProjectAssociations_GetForProjectNoFile(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	assoc := pa.GetForProject("/nonexistent/project")
	if assoc.Path != "/nonexistent/project" {
		t.Fatalf("expected path '/nonexistent/project', got %q", assoc.Path)
	}
	if len(assoc.Associated) != 0 {
		t.Fatalf("expected 0 associations, got %d", len(assoc.Associated))
	}
}

func TestProjectAssociations_FileNamedBySHA256(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	projectPath := "/home/user/my-project"
	pa.Associate(projectPath, "sess-1")

	// Compute expected hash
	h := sha256.Sum256([]byte(projectPath))
	expectedName := fmt.Sprintf("%x", h[:])[:16] + ".json"

	filePath := filepath.Join(dir, "projects", expectedName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("expected file %s to exist: %v", filePath, err)
	}

	var assoc ProjectAssociation
	if err := json.Unmarshal(data, &assoc); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if assoc.Path != projectPath {
		t.Fatalf("expected path %q in file, got %q", projectPath, assoc.Path)
	}
	if len(assoc.Associated) != 1 {
		t.Fatalf("expected 1 association in file, got %d", len(assoc.Associated))
	}
}

func TestProjectAssociations_DisassociateNonexistent(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/proj", "sess-1")
	pa.Disassociate("/proj", "sess-999") // doesn't exist - should be a no-op

	assoc := pa.GetForProject("/proj")
	if len(assoc.Associated) != 1 {
		t.Fatalf("expected 1 association unchanged, got %d", len(assoc.Associated))
	}
}

func TestProjectAssociations_DisassociateAll(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/proj", "sess-1")
	pa.Associate("/proj", "sess-2")
	pa.Disassociate("/proj", "sess-1")
	pa.Disassociate("/proj", "sess-2")

	assoc := pa.GetForProject("/proj")
	if len(assoc.Associated) != 0 {
		t.Fatalf("expected 0 associations after removing all, got %d", len(assoc.Associated))
	}
}

func TestProjectAssociations_Persistence(t *testing.T) {
	dir := t.TempDir()
	pa := NewProjectAssociations(dir)

	pa.Associate("/proj", "sess-1")
	pa.Associate("/proj", "sess-2")

	// Create a new instance from the same directory
	pa2 := NewProjectAssociations(dir)
	assoc := pa2.GetForProject("/proj")
	if len(assoc.Associated) != 2 {
		t.Fatalf("expected 2 associations after reload, got %d", len(assoc.Associated))
	}
}
