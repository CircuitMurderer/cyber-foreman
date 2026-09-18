package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	ErrAdapterNotFound  = errors.New("adapter not found")
	ErrAdapterDuplicate = errors.New("adapter already registered")
)

// Registry exposes the adapters available to the control plane. Adapters own
// their sessions and must therefore be safe to use concurrently across tasks.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) (*Registry, error) {
	registry := &Registry{adapters: make(map[string]Adapter)}
	for _, adapter := range adapters {
		if err := registry.Register(adapter); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return errors.New("adapter is nil")
	}
	name := strings.TrimSpace(adapter.Name())
	if name == "" {
		return errors.New("adapter name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("%w: %s", ErrAdapterDuplicate, name)
	}
	r.adapters[name] = adapter
	return nil
}

func (r *Registry) Get(name string) (Adapter, error) {
	r.mu.RLock()
	adapter, ok := r.adapters[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, name)
	}
	return adapter, nil
}

type Descriptor struct {
	Name         string       `json:"name"`
	Capabilities Capabilities `json:"capabilities"`
}

func (r *Registry) List() []Descriptor {
	r.mu.RLock()
	result := make([]Descriptor, 0, len(r.adapters))
	for name, adapter := range r.adapters {
		result = append(result, Descriptor{Name: name, Capabilities: adapter.Capabilities()})
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
