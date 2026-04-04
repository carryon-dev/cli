package backend

import "sync"

// Registry manages the set of available terminal backends.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NewRegistry creates a new empty backend registry.
func NewRegistry() *Registry {
	return &Registry{
		backends: make(map[string]Backend),
	}
}

// Register adds a backend to the registry.
func (r *Registry) Register(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.ID()] = b
}

// Get returns the backend with the given ID, or nil if not found.
func (r *Registry) Get(id string) Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.backends[id]
}

// GetAvailable returns all backends that report themselves as available.
func (r *Registry) GetAvailable() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []Backend
	for _, b := range r.backends {
		if b.Available() {
			result = append(result, b)
		}
	}
	return result
}

// GetAll returns all registered backends.
func (r *Registry) GetAll() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		result = append(result, b)
	}
	return result
}

// GetDefault returns the preferred backend. If preferredID is set and
// that backend is available, it is returned. Otherwise falls back to
// "native", then to any available backend.
func (r *Registry) GetDefault(preferredID string) Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if preferredID != "" {
		if b, ok := r.backends[preferredID]; ok && b.Available() {
			return b
		}
	}
	if b, ok := r.backends["native"]; ok {
		return b
	}
	// Fall back to any available backend
	for _, b := range r.backends {
		if b.Available() {
			return b
		}
	}
	return nil
}

// ShutdownAll calls Shutdown on every registered backend.
func (r *Registry) ShutdownAll() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.backends {
		b.Shutdown()
	}
}
