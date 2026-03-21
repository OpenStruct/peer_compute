package plugin

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ── Default implementations (open-source / self-hosted) ────────────

// OpenAuth accepts all tokens and returns a fixed user ID.
type OpenAuth struct{}

func (OpenAuth) Authenticate(_ context.Context, token string) (string, error) {
	if token == "" {
		return "anonymous", nil
	}
	return token, nil
}

func (OpenAuth) Issue(_ context.Context, userID string) (string, error) {
	return "tok_" + userID, nil
}

// OpenReputation trusts all providers and renters equally.
type OpenReputation struct{}

func (OpenReputation) ProviderScore(_ context.Context, _ string) (int, error) {
	return 100, nil
}

func (OpenReputation) RenterScore(_ context.Context, _ string) (int, error) {
	return 100, nil
}

func (OpenReputation) RecordOutcome(_ context.Context, _ string, _ bool) error {
	return nil
}

func (OpenReputation) MeetsMinimum(_ context.Context, _ string, _ int) (bool, error) {
	return true, nil
}

// ── MemoryStore ────────────────────────────────────────────────────

var (
	ErrNotFound        = errors.New("not found")
	ErrAmbiguousPrefix = errors.New("ambiguous prefix")
)

// MemoryStore implements Store using in-memory maps.
type MemoryStore struct {
	mu        sync.RWMutex
	providers map[string]*ProviderRecord
	sessions  map[string]*SessionRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		providers: make(map[string]*ProviderRecord),
		sessions:  make(map[string]*SessionRecord),
	}
}

func (m *MemoryStore) Close() error { return nil }

func (m *MemoryStore) PutProvider(_ context.Context, p *ProviderRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[p.ID] = p
	return nil
}

func (m *MemoryStore) GetProvider(_ context.Context, id string) (*ProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[id]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (m *MemoryStore) GetProviderByPrefix(_ context.Context, prefix string) (*ProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.providers[prefix]; ok {
		return p, nil
	}
	var match *ProviderRecord
	for id, p := range m.providers {
		if strings.HasPrefix(id, prefix) {
			if match != nil {
				return nil, ErrAmbiguousPrefix
			}
			match = p
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

func (m *MemoryStore) ListProviders(_ context.Context, filter ProviderFilter) ([]*ProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*ProviderRecord
	for _, p := range m.providers {
		if filter.Status != "" && p.Status != filter.Status {
			continue
		}
		if filter.MinCPU > 0 && p.AvailCPU < filter.MinCPU {
			continue
		}
		if filter.MinMemory > 0 && p.AvailMemoryMB < filter.MinMemory {
			continue
		}
		if filter.MinGPU > 0 && p.AvailGPU < filter.MinGPU {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (m *MemoryStore) UpdateHeartbeat(_ context.Context, providerID string, heartbeat int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.providers[providerID]
	if !ok {
		return ErrNotFound
	}
	p.LastHeartbeat = heartbeat
	p.Status = "online"
	return nil
}

func (m *MemoryStore) DeleteProvider(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.providers, id)
	return nil
}

func (m *MemoryStore) PutSession(_ context.Context, s *SessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}

func (m *MemoryStore) GetSession(_ context.Context, id string) (*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

func (m *MemoryStore) GetSessionByPrefix(_ context.Context, prefix string) (*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[prefix]; ok {
		return s, nil
	}
	var match *SessionRecord
	for id, s := range m.sessions {
		if strings.HasPrefix(id, prefix) {
			if match != nil {
				return nil, ErrAmbiguousPrefix
			}
			match = s
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

func (m *MemoryStore) ListSessions(_ context.Context, filter SessionFilter) ([]*SessionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*SessionRecord
	for _, s := range m.sessions {
		if filter.ProviderID != "" && s.ProviderID != filter.ProviderID {
			continue
		}
		if filter.RenterID != "" && s.RenterID != filter.RenterID {
			continue
		}
		if filter.Status != "" && s.Status != filter.Status {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

func (m *MemoryStore) GetTerminatedSessionIDs(_ context.Context, sessionIDs []string, providerID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var terminated []string
	for _, sid := range sessionIDs {
		s, ok := m.sessions[sid]
		if !ok {
			continue
		}
		if s.ProviderID == providerID && s.Status == "terminated" {
			terminated = append(terminated, s.ID)
		}
	}
	return terminated, nil
}

// ── Bundle ─────────────────────────────────────────────────────────

// Defaults returns a set of default plugin implementations for the
// open-source / self-hosted version.
func Defaults() *Bundle {
	return &Bundle{
		Auth:       OpenAuth{},
		Reputation: OpenReputation{},
		Store:      NewMemoryStore(),
	}
}

// Bundle holds all plugin implementations together.
type Bundle struct {
	Auth       Authenticator
	Reputation Reputation
	Store      Store
}
