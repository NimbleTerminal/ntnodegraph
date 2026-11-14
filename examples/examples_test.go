// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package examples_test

import (
	"testing"

	"nimbleterminal.dev/ntnodegraph"
	"nimbleterminal.dev/ntnodegraph/editor"
	"nimbleterminal.dev/ntnodegraph/examples/internal/catalog"
)

func TestCatalogPresetsAndSubgraphFlatten(t *testing.T) {
	env := catalog.New()
	p := env.Backend

	// 1. EditorSpec validates cleanly including agent/* cold-inlet state feedback.
	edSpec := catalog.EditorSpec()
	report, err := ntnodegraph.Validate(edSpec, env.Registry, ntnodegraph.ValidateOptions{Backend: &p})
	if err != nil {
		t.Fatalf("Validate(EditorSpec): %v", err)
	}
	if len(report.TopologicalOrder) != len(edSpec.Nodes) {
		t.Fatalf("TopologicalOrder = %d, want %d", len(report.TopologicalOrder), len(edSpec.Nodes))
	}

	// 2. SubgraphSpec flattens graph/svg-critic-loop@1 + graph/cinema-grade@1 cleanly.
	subSpec := catalog.SubgraphSpec()
	flat, flatReport, err := ntnodegraph.Flatten(subSpec, env.Registry, env.Library, ntnodegraph.FlattenOptions{Backend: &p})
	if err != nil {
		t.Fatalf("Flatten(SubgraphSpec): %v", err)
	}
	if flatReport.ExpandedInstances != 2 {
		t.Fatalf("ExpandedInstances = %d, want 2", flatReport.ExpandedInstances)
	}
	if len(flat.Nodes) != 10 {
		t.Fatalf("flattened node count = %d, want 10", len(flat.Nodes))
	}

	// 3. Editor renders both Vector and Grid modes with seeded previews and drill-in.
	ed := editor.New(subSpec, env.Registry, editor.Options{
		BackendProfile:      env.Backend,
		Library:             env.Library,
		OnRegisterComponent: env.RegisterComponent,
		Mode:                editor.RenderGrid,
		Width:               100,
		Height:              28,
	})
	catalog.SeedPreviews(ed)
	if view := ed.View(); view == "" {
		t.Fatal("expected non-empty Grid view")
	}
	if _, err := ed.DrillIntoSubgraph("synth"); err != nil {
		t.Fatalf("DrillIntoSubgraph(synth): %v", err)
	}
	if ed.Depth() != 1 {
		t.Fatalf("Depth = %d, want 1", ed.Depth())
	}
}
