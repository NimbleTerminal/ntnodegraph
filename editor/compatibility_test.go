// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package editor

import (
	"testing"

	ng "nimbleterminal.dev/ntnodegraph"
)

func TestHostPolicyControlsWirePreviewAndCommit(t *testing.T) {
	reg := ng.NewRegistry()
	source := ng.MustParseTypeID("test/source@1")
	sink := ng.MustParseTypeID("test/sink@1")
	reg.MustRegister(ng.TypeDef{ID: source, Outlets: []ng.PortDef{{Name: "out", Type: ng.TypeString}}})
	reg.MustRegister(ng.TypeDef{ID: sink, Inlets: []ng.PortDef{{Name: "in", Type: ng.TypeBytes}}})
	sp, _ := ng.NewSpec("policy")
	sp.Nodes["source"] = ng.NodeSpec{Type: source}
	sp.Nodes["sink"] = ng.NodeSpec{Type: sink}
	backend := ng.BackendProfile{Compatibility: ng.ExactCompatibility}
	m := New(sp, reg, Options{BackendProfile: backend, Mode: RenderGrid})
	drag := &WireDragPreview{Active: true, Anchor: ng.MustParsePortRef("source.out"), AnchorType: ng.TypeString, FromOutlet: true}
	scene := BuildSceneWithOptions(sp, reg, Selection{}, drag, m.currentSceneOptions())
	if got := scene.Nodes[scene.NodeByID["sink"]].Inlets[0].CompatState; got != PortCompatInvalid {
		t.Fatalf("preview accepted conversion: %v", got)
	}
	if _, err := m.ApplyEdits(ng.Connect(ng.MustParsePortRef("source.out"), ng.MustParsePortRef("sink.in"), "")); err == nil {
		t.Fatal("commit ignored unnamed backend policy")
	}
}
