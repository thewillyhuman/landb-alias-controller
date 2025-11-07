package openstack

import (
	"sort"
	"sync"
)

// PropertySet represents a deterministic, thread-safe, ordered set of properties.
// It preserves key order for consistent iteration, serialization, and comparison.
type PropertySet struct {
	// mu is a mutex to protect the data and keys fields.
	mu sync.RWMutex
	// data is the underlying map of properties.
	data map[string]string
	// keys is the list of keys in deterministic order.
	keys []string
	// dirty marks if keys need re-sorting.
	dirty bool
}

// NewPropertySet creates a new PropertySet from an optional initial map.
func NewPropertySet(initial map[string]string) *PropertySet {
	ps := &PropertySet{
		data: make(map[string]string, len(initial)),
	}
	for k, v := range initial {
		ps.data[k] = v
		ps.keys = append(ps.keys, k)
	}
	sort.Strings(ps.keys)
	return ps
}

// Set adds or updates a key while preserving deterministic ordering.
func (p *PropertySet) Set(key, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.data[key]; !exists {
		p.keys = append(p.keys, key)
		p.dirty = true
	}
	p.data[key] = value
}

// Get retrieves a value by key.
func (p *PropertySet) Get(key string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	val, ok := p.data[key]
	return val, ok
}

// Delete removes a key if it exists.
func (p *PropertySet) Delete(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.data, key)
	p.dirty = true
}

// Keys returns the keys in deterministic order.
func (p *PropertySet) Keys() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.dirty {
		sort.Strings(p.keys)
		p.dirty = false
	}
	cpy := make([]string, len(p.keys))
	copy(cpy, p.keys)
	return cpy
}

// Clone creates a deep copy of the PropertySet.
func (p *PropertySet) Clone() *PropertySet {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cpy := make(map[string]string, len(p.data))
	for k, v := range p.data {
		cpy[k] = v
	}
	return NewPropertySet(cpy)
}

// AsMap returns a shallow copy of the underlying map (unsorted).
func (p *PropertySet) AsMap() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cpy := make(map[string]string, len(p.data))
	for k, v := range p.data {
		cpy[k] = v
	}
	return cpy
}

// ForEach iterates over properties in deterministic order.
func (p *PropertySet) ForEach(fn func(key, value string)) {
	for _, k := range p.Keys() {
		fn(k, p.data[k])
	}
}

// ToMetadataUpdateMap returns a map[string]any suitable for
// OpenStack CreateMetadatum calls.
func (p *PropertySet) ToMetadataUpdateMap() (map[string]any, error) {
	return map[string]any{"metadata": p.AsMap()}, nil
}
