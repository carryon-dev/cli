package project

import (
	"github.com/carryon-dev/cli/internal/backend"
)

// TerminalMatch represents the result of matching a declared terminal
// against running sessions.
type TerminalMatch struct {
	Declared DeclaredTerminal `json:"declared"`
	Status   string           `json:"status"` // "running" or "missing"
	Session  *backend.Session `json:"session,omitempty"`
}

// MatchTerminals matches declared terminals against running sessions.
// For each declared terminal, it finds a running session with a matching
// name. If found, the status is "running"; otherwise "missing".
func MatchTerminals(declared []DeclaredTerminal, running []backend.Session) []TerminalMatch {
	matches := make([]TerminalMatch, 0, len(declared))

	for _, d := range declared {
		match := TerminalMatch{
			Declared: d,
			Status:   "missing",
		}

		for i := range running {
			if running[i].Name == d.Name {
				match.Status = "running"
				match.Session = &running[i]
				break
			}
		}

		matches = append(matches, match)
	}

	return matches
}
