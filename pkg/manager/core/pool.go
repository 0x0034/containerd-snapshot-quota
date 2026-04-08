package core

import (
	"fmt"
	"sync"
)

// ProjectIDPool manages allocation and release of project IDs.
type ProjectIDPool struct {
	mu    sync.Mutex
	min   uint32
	max   uint32
	inUse map[uint32]bool
}

// NewProjectIDPool creates a pool with the given range [min, max].
func NewProjectIDPool(min, max uint32) *ProjectIDPool {
	return &ProjectIDPool{
		min:   min,
		max:   max,
		inUse: make(map[uint32]bool),
	}
}

// Allocate returns the next available project ID.
func (p *ProjectIDPool) Allocate() (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id := p.min; id <= p.max; id++ {
		if !p.inUse[id] {
			p.inUse[id] = true
			return id, nil
		}
	}
	return 0, fmt.Errorf("project ID pool exhausted (range %d-%d)", p.min, p.max)
}

// Release returns a project ID back to the pool.
func (p *ProjectIDPool) Release(id uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inUse, id)
}

// MarkUsed marks a project ID as in-use (for recovery).
func (p *ProjectIDPool) MarkUsed(id uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inUse[id] = true
}

// IsUsed checks whether a project ID is currently allocated.
func (p *ProjectIDPool) IsUsed(id uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inUse[id]
}
