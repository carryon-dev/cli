package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectConfig represents a .carryon.json project configuration file.
type ProjectConfig struct {
	Version   int             `json:"version"`
	Terminals []TerminalEntry `json:"terminals"`
}

// TerminalEntry is either a single terminal or a split group.
type TerminalEntry struct {
	// Single is set when the entry is a single terminal object.
	Single *DeclaredTerminal
	// Group is set when the entry is an array of terminals (split group).
	Group []DeclaredTerminal
}

func (e *TerminalEntry) UnmarshalJSON(data []byte) error {
	// Try array first (split group)
	var group []DeclaredTerminal
	if err := json.Unmarshal(data, &group); err == nil {
		e.Group = group
		return nil
	}
	// Try single terminal
	var single DeclaredTerminal
	if err := json.Unmarshal(data, &single); err == nil {
		e.Single = &single
		return nil
	}
	return fmt.Errorf("terminal entry must be an object or array of objects")
}

func (e TerminalEntry) MarshalJSON() ([]byte, error) {
	if e.Group != nil {
		return json.Marshal(e.Group)
	}
	if e.Single != nil {
		return json.Marshal(e.Single)
	}
	return []byte("null"), nil
}

// AllTerminals returns all declared terminals flattened from entries.
func (c *ProjectConfig) AllTerminals() []DeclaredTerminal {
	var result []DeclaredTerminal
	for _, entry := range c.Terminals {
		if entry.Single != nil {
			result = append(result, *entry.Single)
		}
		result = append(result, entry.Group...)
	}
	return result
}

// DeclaredTerminal represents a terminal declared in the project config.
type DeclaredTerminal struct {
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	Backend string `json:"backend,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Color   string `json:"color,omitempty"`
	Icon    string `json:"icon,omitempty"`
}

// ParseProjectConfig parses a raw JSON string into a ProjectConfig,
// returning an error if validation fails.
func ParseProjectConfig(raw string) (*ProjectConfig, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	errors := ValidateProjectConfig(obj)
	if len(errors) > 0 {
		return nil, fmt.Errorf("invalid .carryon.json: %s", strings.Join(errors, "; "))
	}

	var cfg ProjectConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

// ValidateProjectConfig validates a parsed JSON object and returns a list
// of error strings. An empty list means the config is valid.
func ValidateProjectConfig(obj map[string]any) []string {
	var errors []string

	if obj == nil {
		return []string{"Must be a JSON object"}
	}

	version, hasVersion := obj["version"]
	if !hasVersion {
		errors = append(errors, "Missing required field: version")
	} else {
		// JSON numbers unmarshal as float64
		v, ok := version.(float64)
		if !ok || v != 1 {
			errors = append(errors, fmt.Sprintf("Unsupported version: %v (expected 1)", version))
		}
	}

	terminals, hasTerminals := obj["terminals"]
	if !hasTerminals {
		errors = append(errors, "Missing required field: terminals (must be an array)")
	} else {
		arr, ok := terminals.([]any)
		if !ok {
			errors = append(errors, "Missing required field: terminals (must be an array)")
		} else {
			for i, item := range arr {
				switch t := item.(type) {
				case map[string]any:
					// Single terminal
					name, hasName := t["name"]
					if !hasName {
						errors = append(errors, fmt.Sprintf("terminals[%d]: missing required field: name", i))
					} else if _, ok := name.(string); !ok || name == "" {
						errors = append(errors, fmt.Sprintf("terminals[%d]: missing required field: name", i))
					}
				case []any:
					// Split group
					if len(t) == 0 {
						errors = append(errors, fmt.Sprintf("terminals[%d]: split group must not be empty", i))
						continue
					}
					for j, groupItem := range t {
						gt, ok := groupItem.(map[string]any)
						if !ok {
							errors = append(errors, fmt.Sprintf("terminals[%d][%d]: must be an object", i, j))
							continue
						}
						name, hasName := gt["name"]
						if !hasName {
							errors = append(errors, fmt.Sprintf("terminals[%d][%d]: missing required field: name", i, j))
						} else if _, ok := name.(string); !ok || name == "" {
							errors = append(errors, fmt.Sprintf("terminals[%d][%d]: missing required field: name", i, j))
						}
					}
				default:
					errors = append(errors, fmt.Sprintf("terminals[%d]: must be an object or array", i))
				}
			}
		}
	}

	return errors
}

// ReadProjectConfig reads a .carryon.json file from the given directory.
// Returns nil if the file is missing or invalid.
func ReadProjectConfig(projectPath string) *ProjectConfig {
	configPath := filepath.Join(projectPath, ".carryon.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	cfg, err := ParseProjectConfig(string(data))
	if err != nil {
		return nil
	}

	return cfg
}
