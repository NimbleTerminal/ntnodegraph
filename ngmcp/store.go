// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	nodegraph "nimbleterminal.dev/ntnodegraph"
)

// Sentinel errors returned by DocumentStore implementations.
var (
	ErrConflict       = errors.New("ngmcp: document revision conflict")
	ErrTargetRequired = errors.New("ngmcp: target graph name or path is required")
	ErrGraphNotFound  = errors.New("ngmcp: graph target not found")
)

// PutOptions carries the required revision precondition and optional persistence
// flags passed to a DocumentStore implementation.
type PutOptions struct {
	// ExpectedRevision must match Get; zero requires the target to be absent.
	ExpectedRevision uint64
	Create           bool
	Save             bool
}

// PutResult carries optional host feedback returned when committing an edited
// Spec (for example, live hot-reload telemetry when connected to Nebula Studio).
type PutResult struct {
	Revision uint64         `json:"revision"`
	Target   string         `json:"target,omitempty"`
	Saved    bool           `json:"saved,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// DocumentStore abstracts reading and transactionally persisting graph Specs
// so ngmcp remains completely free of filesystem and runtime dependencies.
type DocumentStore interface {
	// DefaultTarget returns the default target identifier when a tool call omits "target".
	DefaultTarget() string

	// ListTargets returns sorted target identifiers currently known to the store.
	ListTargets(ctx context.Context) ([]string, error)

	// Get returns a defensive clone and its nonzero revision. Revisions must change
	// on every write, including writes outside MCP, and must never be reused.
	// Missing targets return revision zero and ErrGraphNotFound.
	Get(ctx context.Context, target string) (*nodegraph.Spec, uint64, error)

	// Put commits next only if ExpectedRevision matches the current revision.
	// The comparison and commit must be atomic; stale writes return ErrConflict.
	// diff must be computed against that same revision.
	// Implementations must reject invalid commits atomically without leaving
	// partial mutations behind.
	Put(ctx context.Context, target string, next *nodegraph.Spec, diff nodegraph.SpecDiff, opts PutOptions) (PutResult, error)
}

// MemoryStore is a concurrency-safe, deterministic in-memory DocumentStore
// suitable for pure-Go hosts, tests, and GOOS=js GOARCH=wasm builds.
type MemoryStore struct {
	mu            sync.RWMutex
	defaultTarget string
	specs         map[string]*nodegraph.Spec
	revisions     map[string]uint64
}

// NewMemoryStore creates an initialized MemoryStore with optional initial specs.
func NewMemoryStore(defaultTarget string, initial map[string]*nodegraph.Spec) *MemoryStore {
	m := &MemoryStore{
		defaultTarget: defaultTarget,
		specs:         make(map[string]*nodegraph.Spec, len(initial)),
		revisions:     make(map[string]uint64),
	}
	for _, k := range slices.Sorted(maps.Keys(initial)) {
		sp := initial[k]
		if sp != nil {
			m.specs[k] = sp.Clone().Canonicalize()
			m.revisions[k] = 1
			if m.defaultTarget == "" {
				m.defaultTarget = k
			}
		}
	}
	return m
}

// DefaultTarget implements DocumentStore.
func (m *MemoryStore) DefaultTarget() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultTarget
}

// ListTargets implements DocumentStore in deterministic sorted order.
func (m *MemoryStore) ListTargets(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Sorted(maps.Keys(m.specs)), nil
}

// Get implements DocumentStore.
func (m *MemoryStore) Get(_ context.Context, target string) (*nodegraph.Spec, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if target == "" {
		target = m.defaultTarget
	}
	if target == "" {
		return nil, 0, ErrTargetRequired
	}
	sp, ok := m.specs[target]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %q", ErrGraphNotFound, target)
	}
	return sp.Clone(), m.revisions[target], nil
}

// Put implements DocumentStore.
func (m *MemoryStore) Put(_ context.Context, target string, next *nodegraph.Spec, _ nodegraph.SpecDiff, opts PutOptions) (PutResult, error) {
	if next == nil {
		return PutResult{}, fmt.Errorf("%w: nil spec", nodegraph.ErrInvalidSpec)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if target == "" {
		target = m.defaultTarget
	}
	if target == "" {
		return PutResult{}, ErrTargetRequired
	}
	if m.revisions[target] != opts.ExpectedRevision {
		return PutResult{}, fmt.Errorf("%w: %q", ErrConflict, target)
	}
	if _, exists := m.specs[target]; !exists && !opts.Create {
		return PutResult{}, fmt.Errorf("%w: %q (pass create=true to initialize a new graph)", ErrGraphNotFound, target)
	}
	m.specs[target] = next.Clone().Canonicalize()
	m.revisions[target]++
	if m.defaultTarget == "" {
		m.defaultTarget = target
	}
	return PutResult{Target: target, Saved: true, Revision: m.revisions[target]}, nil
}
