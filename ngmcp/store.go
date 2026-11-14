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
	ErrTargetRequired = errors.New("ngmcp: target graph name or path is required")
	ErrGraphNotFound  = errors.New("ngmcp: graph target not found")
)

// PutOptions carries optional persistence flags passed from the "edit" tool to
// a DocumentStore implementation.
type PutOptions struct {
	Create bool
	Save   bool
}

// PutResult carries optional host feedback returned when committing an edited
// Spec (for example, live hot-reload telemetry when connected to Nebula Studio).
type PutResult struct {
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

	// Get returns a defensive clone of the Spec identified by target.
	Get(ctx context.Context, target string) (*nodegraph.Spec, error)

	// Put commits next (with diff computed against the prior Spec) to target.
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
}

// NewMemoryStore creates an initialized MemoryStore with optional initial specs.
func NewMemoryStore(defaultTarget string, initial map[string]*nodegraph.Spec) *MemoryStore {
	m := &MemoryStore{
		defaultTarget: defaultTarget,
		specs:         make(map[string]*nodegraph.Spec, len(initial)),
	}
	for k, sp := range initial {
		if sp != nil {
			m.specs[k] = sp.Clone().Canonicalize()
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
func (m *MemoryStore) Get(_ context.Context, target string) (*nodegraph.Spec, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if target == "" {
		target = m.defaultTarget
	}
	if target == "" {
		return nil, ErrTargetRequired
	}
	sp, ok := m.specs[target]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrGraphNotFound, target)
	}
	return sp.Clone(), nil
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
	if _, exists := m.specs[target]; !exists && !opts.Create {
		return PutResult{}, fmt.Errorf("%w: %q (pass create=true to initialize a new graph)", ErrGraphNotFound, target)
	}
	m.specs[target] = next.Clone().Canonicalize()
	if m.defaultTarget == "" {
		m.defaultTarget = target
	}
	return PutResult{Target: target, Saved: true}, nil
}
