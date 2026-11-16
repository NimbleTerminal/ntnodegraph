// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ngmcp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	ng "nimbleterminal.dev/ntnodegraph"
)

// Four simulated clients retain stale snapshots across writes. Compare every
// transition against a small revision model, including failed creates and puts.
func FuzzMemoryStoreTransactions(f *testing.F) {
	f.Add([]byte{1, 0, 4, 2, 6, 1, 5, 0, 2})
	f.Add([]byte{1, 1, 1, 1, 0, 2, 2, 2})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 256 {
			t.Skip()
		}
		ctx := context.Background()
		store := NewMemoryStore("graph", nil)
		var revision uint64
		var description string
		var clients [4]uint64
		var snapshots [4]*ng.Spec
		for i, op := range ops {
			client := int(op>>2) % len(clients)
			switch op & 3 {
			case 0:
				sp, rev, err := store.Get(ctx, "graph")
				if revision == 0 {
					if !errors.Is(err, ErrGraphNotFound) || rev != 0 {
						t.Fatalf("missing read: rev=%d err=%v", rev, err)
					}
				} else {
					if err != nil || rev != revision || sp.Description != description {
						t.Fatalf("read disagrees with model: rev=%d err=%v", rev, err)
					}
					snapshots[client] = sp.Clone()
					sp.Description = "caller mutation"
				}
				clients[client] = rev
			default:
				next, _ := ng.NewSpec("graph")
				next.Description = fmt.Sprintf("write%d", i)
				create := op&3 == 1
				res, err := store.Put(ctx, "graph", next, ng.Diff(snapshots[client], next), PutOptions{Create: create, ExpectedRevision: clients[client]})
				if clients[client] != revision {
					if !errors.Is(err, ErrConflict) {
						t.Fatalf("stale write accepted: %v", err)
					}
				} else if revision == 0 && !create {
					if !errors.Is(err, ErrGraphNotFound) {
						t.Fatalf("missing write: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					revision++
					description = next.Description
					if res.Revision != revision {
						t.Fatalf("revision=%d want %d", res.Revision, revision)
					}
					next.Description = "caller mutation"
				}
			}
			got, rev, err := store.Get(ctx, "graph")
			if revision == 0 {
				if !errors.Is(err, ErrGraphNotFound) {
					t.Fatal(err)
				}
				continue
			}
			if err != nil || rev != revision || got.Description != description {
				t.Fatalf("state changed unexpectedly after op %d: rev=%d err=%v", i, rev, err)
			}
		}
	})
}
