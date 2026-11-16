// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	ng "nimbleterminal.dev/ntnodegraph"
)

type synchronizedReadStore struct {
	*MemoryStore
	reads sync.WaitGroup
}

func (s *synchronizedReadStore) Get(ctx context.Context, target string) (*ng.Spec, uint64, error) {
	sp, rev, err := s.MemoryStore.Get(ctx, target)
	s.reads.Done()
	s.reads.Wait()
	return sp, rev, err
}

func TestConcurrentEditsRejectStaleRevision(t *testing.T) {
	ctx := context.Background()
	sp, _ := ng.NewSpec("main")
	store := &synchronizedReadStore{MemoryStore: NewMemoryStore("main", map[string]*ng.Spec{"main": sp})}
	store.reads.Add(2)
	reg := ng.NewRegistry()
	reg.MustRegister(ng.TypeDef{ID: ng.MustParseTypeID("test/node@1")})
	server := NewServer(Options{Store: store, Registry: reg})
	results := make(chan CallToolResult, 2)
	for _, id := range []string{"a", "b"} {
		go func() {
			result, err := server.handleEdit(ctx, []byte(fmt.Sprintf(`{"ops":[{"op":"add_node","id":%q,"type":"test/node@1"}]}`, id)))
			if err != nil {
				result = ErrorResult(err, nil)
			}
			results <- result
		}()
	}
	conflicts := 0
	for range 2 {
		result := <-results
		if result.IsError {
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, ErrConflict.Error()) {
				t.Fatalf("unexpected failure: %+v", result)
			}
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("got %d conflicts, want one", conflicts)
	}
	current, rev, err := store.MemoryStore.Get(ctx, "main")
	if err != nil || len(current.Nodes) != 1 || rev != 2 {
		t.Fatalf("unexpected committed state: %v rev=%d err=%v", current, rev, err)
	}
}

func TestMemoryStoreRevisionAndDefault(t *testing.T) {
	ctx := context.Background()
	a, _ := ng.NewSpec("a")
	z, _ := ng.NewSpec("z")
	for range 100 {
		store := NewMemoryStore("", map[string]*ng.Spec{"z": z, "a": a})
		if store.DefaultTarget() != "a" {
			t.Fatal("default target must be lexically first")
		}
	}
	store := NewMemoryStore("a", nil)
	if _, err := store.Put(ctx, "a", a, ng.SpecDiff{}, PutOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "a", z, ng.SpecDiff{}, PutOptions{Create: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create overwrote existing document: %v", err)
	}
	before, rev, _ := store.Get(ctx, "a")
	next := before.Clone()
	next.Description = "updated"
	if _, err := store.Put(ctx, "a", next, ng.Diff(before, next), PutOptions{ExpectedRevision: rev}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "a", before, ng.SpecDiff{}, PutOptions{ExpectedRevision: rev}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	got, _, _ := store.Get(ctx, "a")
	if got.Description != "updated" {
		t.Fatal("conflicting write mutated store")
	}
}
