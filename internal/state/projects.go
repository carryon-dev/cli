package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ProjectAssociation holds the association between a project path and sessions.
type ProjectAssociation struct {
	Path       string              `json:"path"`
	Associated []AssociatedSession `json:"associated"`
}

// AssociatedSession records a session linked to a project.
type AssociatedSession struct {
	SessionID string `json:"sessionId"`
	AddedAt   string `json:"addedAt"`
}

// ProjectAssociations manages per-project session associations stored as JSON files.
type ProjectAssociations struct {
	mu          sync.Mutex
	projectsDir string
}

// NewProjectAssociations creates a ProjectAssociations manager.
func NewProjectAssociations(stateDir string) *ProjectAssociations {
	return &ProjectAssociations{
		projectsDir: filepath.Join(stateDir, "projects"),
	}
}

// Associate links a session to a project. Idempotent - does nothing if already associated.
func (pa *ProjectAssociations) Associate(projectPath, sessionID string) error {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	data := pa.getForProject(projectPath)
	for _, a := range data.Associated {
		if a.SessionID == sessionID {
			return nil
		}
	}
	data.Associated = append(data.Associated, AssociatedSession{
		SessionID: sessionID,
		AddedAt:   time.Now().UTC().Format(time.RFC3339),
	})
	return pa.saveForProject(projectPath, data)
}

// Disassociate removes a session from a project's associations.
func (pa *ProjectAssociations) Disassociate(projectPath, sessionID string) error {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	data := pa.getForProject(projectPath)
	filtered := make([]AssociatedSession, 0, len(data.Associated))
	for _, a := range data.Associated {
		if a.SessionID != sessionID {
			filtered = append(filtered, a)
		}
	}
	data.Associated = filtered
	return pa.saveForProject(projectPath, data)
}

// GetForProject returns the association for a project path. Returns an empty
// association (with the path set) if none exists.
func (pa *ProjectAssociations) GetForProject(projectPath string) ProjectAssociation {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return pa.getForProject(projectPath)
}

func (pa *ProjectAssociations) getForProject(projectPath string) ProjectAssociation {
	filePath := pa.filePathFor(projectPath)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ProjectAssociation{Path: projectPath, Associated: []AssociatedSession{}}
	}
	var assoc ProjectAssociation
	if err := json.Unmarshal(data, &assoc); err != nil {
		return ProjectAssociation{Path: projectPath, Associated: []AssociatedSession{}}
	}
	return assoc
}

func (pa *ProjectAssociations) saveForProject(projectPath string, data ProjectAssociation) error {
	if err := os.MkdirAll(pa.projectsDir, 0700); err != nil {
		return fmt.Errorf("create projects dir: %w", err)
	}
	filePath := pa.filePathFor(projectPath)
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project association: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filePath, raw, 0600); err != nil {
		return fmt.Errorf("write project association: %w", err)
	}
	return nil
}

func (pa *ProjectAssociations) filePathFor(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	hash := fmt.Sprintf("%x", h[:])[:16]
	return filepath.Join(pa.projectsDir, hash+".json")
}
