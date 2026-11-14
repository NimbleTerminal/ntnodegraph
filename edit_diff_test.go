// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"slices"
	"testing"
)

func TestHeadlessEditsAndStructuralDiff(t *testing.T) {
	base, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	// 1. UI-only edits (move, label, group) must report HasRuntimeChanges() == false.
	uiEdited, err := ApplyEdits(
		base,
		MoveNode("hn", 80, 160),
		SetNodeLabel("hn", "Hacker News Top Stories"),
		PutGroup("ingest", GroupSpec{
			Label: "Ingest & Typeset",
			Nodes: []string{"typeset", "hn"},
			UI:    &GroupUI{X: 20, Y: 80, Width: 420, Height: 180},
		}),
	)
	if err != nil {
		t.Fatalf("ApplyEdits(UI): %v", err)
	}

	uiDiff := Diff(base, uiEdited)
	if uiDiff.IsEmpty() {
		t.Fatal("expected non-empty SpecDiff for UI edits")
	}
	if uiDiff.HasRuntimeChanges() {
		t.Fatalf("UI-only edits must not flag HasRuntimeChanges(): %+v", uiDiff)
	}
	if !uiDiff.GroupsChanged || len(uiDiff.ModifiedNodes) != 1 || uiDiff.ModifiedNodes[0].ID != "hn" || !uiDiff.ModifiedNodes[0].UIChanged {
		t.Fatalf("unexpected UI diff: %+v", uiDiff)
	}

	// 2. Runtime-affecting edits (param change, node removal) flag HasRuntimeChanges() == true.
	rtEdited, err := ApplyEdits(
		uiEdited,
		SetParam("typeset", "limit", 15),
		RemoveNode("view"),
	)
	if err != nil {
		t.Fatalf("ApplyEdits(runtime): %v", err)
	}
	rtDiff := Diff(uiEdited, rtEdited)
	if !rtDiff.HasRuntimeChanges() {
		t.Fatalf("expected HasRuntimeChanges() == true, got %+v", rtDiff)
	}
	if !slices.Equal(rtDiff.RemovedNodes, []string{"view"}) {
		t.Fatalf("RemovedNodes = %v, want [view]", rtDiff.RemovedNodes)
	}
	if len(rtDiff.RemovedEdges) != 1 || rtDiff.RemovedEdges[0].To.Node != "view" {
		t.Fatalf("expected cascade edge removal for view, got %+v", rtDiff.RemovedEdges)
	}
	if len(rtDiff.ModifiedNodes) != 1 || !slices.Equal(rtDiff.ModifiedNodes[0].ChangedParams, []string{"limit"}) {
		t.Fatalf("expected modified param [limit], got %+v", rtDiff.ModifiedNodes)
	}

	// 3. Failed transactional edit leaves input spec untouched.
	origTypesetLimit := base.Nodes["typeset"].Params["limit"]
	_, err = ApplyEdits(
		base,
		SetParam("typeset", "limit", 99),
		MoveNode("does-not-exist", 10, 10),
	)
	if !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("expected ErrUnknownNode, got %v", err)
	}
	if base.Nodes["typeset"].Params["limit"] != origTypesetLimit {
		t.Fatalf("ApplyEdits mutated input spec on failure")
	}

	// 4. Direct AddNode(...).Apply on a zero Spec allocates Nodes instead of panicking,
	// and nil Spec inputs fail cleanly with ErrInvalidSpec.
	var zeroSpec Spec
	if err := AddNode("n1", NodeSpec{Type: MustParseTypeID("source/timer@1")}).Apply(&zeroSpec); err != nil {
		t.Fatalf("AddNode on zero Spec failed: %v", err)
	}
	if _, ok := zeroSpec.Nodes["n1"]; !ok {
		t.Fatalf("expected n1 in zeroSpec.Nodes")
	}
	if err := AddNode("n2", NodeSpec{Type: MustParseTypeID("source/timer@1")}).Apply(nil); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("AddNode(nil) = %v, want ErrInvalidSpec", err)
	}
	if err := RemoveNode("n1").Apply(nil); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("RemoveNode(nil) = %v, want ErrInvalidSpec", err)
	}

	// 5. Diff detects Version changes as runtime-affecting changes.
	v2 := base.Clone()
	v2.Version = 2
	vDiff := Diff(base, v2)
	if !vDiff.VersionChanged || vDiff.IsEmpty() || !vDiff.HasRuntimeChanges() {
		t.Fatalf("expected VersionChanged runtime diff, got %+v", vDiff)
	}
}
