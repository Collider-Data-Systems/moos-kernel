package kernel

import (
	"sync"

	"moos/kernel/internal/graph"
)

// MemStore is an in-memory Store implementation.
// Used for tests and ephemeral kernel instances.
// Not persistent across restarts.
type MemStore struct {
	mu  sync.Mutex
	log []graph.PersistedRewrite
}

func NewMemStore() *MemStore {
	return &MemStore{}
}

func (m *MemStore) Append(entries []graph.PersistedRewrite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, entries...)
	return nil
}

func (m *MemStore) ReadAll() ([]graph.PersistedRewrite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]graph.PersistedRewrite, len(m.log))
	copy(cp, m.log)
	return cp, nil
}

// SingleWriter is always true for an in-memory store: there is no file for a
// second process to share, so the multi-writer seq race cannot arise.
func (m *MemStore) SingleWriter() bool { return true }
