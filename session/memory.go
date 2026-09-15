package session

import (
	"context"
	"fmt"
	"math"
	"sync"
)

type Memory struct {
	mu   sync.Mutex
	data map[Key]*Snapshot
}

func NewMemory() *Memory { return &Memory{data: map[Key]*Snapshot{}} }
func (m *Memory) Load(ctx context.Context, key Key) (*Snapshot, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return Clone(s)
}
func (m *Memory) Save(ctx context.Context, s *Snapshot) (*Snapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s != nil && s.Version == math.MaxInt64 {
		return nil, fmt.Errorf("session version exhausted")
	}
	candidate, err := Clone(s)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := m.data[s.Key]
	if current == nil && s.Version != 0 || current != nil && current.Version != s.Version {
		return nil, ErrConflict
	}
	candidate.Version++
	returned, err := Clone(candidate)
	if err != nil {
		return nil, err
	}
	if m.data == nil {
		m.data = map[Key]*Snapshot{}
	}
	m.data[s.Key] = candidate
	return returned, nil
}
