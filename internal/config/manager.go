package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Manager struct {
	mu          sync.RWMutex
	baseDir     string
	configPath  string
	values      map[string]any
	lastWarning string
}

func NewManager(baseDir string) *Manager {
	m := &Manager{
		baseDir:    baseDir,
		configPath: filepath.Join(baseDir, "config.json"),
		values:     GetAllDefaults(),
	}
	m.load()
	return m
}

func (m *Manager) GetString(key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.values[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	if def, ok := Schema[key]; ok {
		if s, ok := def.defVal.(string); ok {
			return s
		}
	}
	return ""
}

func (m *Manager) GetInt(key string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.values[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case float64:
			return int(n)
		}
	}
	if def, ok := Schema[key]; ok {
		if n, ok := def.defVal.(int); ok {
			return n
		}
	}
	return 0
}

func (m *Manager) GetBool(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.values[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	if def, ok := Schema[key]; ok {
		if b, ok := def.defVal.(bool); ok {
			return b
		}
	}
	return false
}

func (m *Manager) Get(key string) any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.values[key]; ok {
		return v
	}
	if def, ok := Schema[key]; ok {
		return def.defVal
	}
	return nil
}

func (m *Manager) Set(key, rawValue string) error {
	val, err := ValidateKey(key, rawValue)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.values[key] = val
	m.lastWarning = ""
	if key == "local.expose" && rawValue == "true" {
		m.lastWarning = "Exposing the web server beyond localhost (binding to all interfaces)"
	}
	m.mu.Unlock()

	return m.save()
}

func (m *Manager) LastWarning() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastWarning
}

func (m *Manager) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		m.values = GetAllDefaults()
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		m.values = GetAllDefaults()
		return
	}

	// Merge file values over defaults, preserving any runtime-set keys
	// that are not present in the file.
	defaults := GetAllDefaults()
	for key, value := range raw {
		defaults[key] = value
	}
	m.values = defaults
}

func (m *Manager) FilePath() string {
	return m.configPath
}

func (m *Manager) load() {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for key, value := range raw {
		if _, ok := Schema[key]; ok {
			m.values[key] = value
		}
	}
}

func (m *Manager) save() error {
	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.values, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename to avoid corruption on crash.
	tmp := m.configPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.configPath)
}
