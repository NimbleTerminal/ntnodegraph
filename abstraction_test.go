// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"testing"
)

func TestOpaqueTypesAndHostCompatibility(t *testing.T) {
	for _, name := range []string{"audio/pcm", "finance/order.v2"} {
		dt, err := ParseDataType(name)
		if err != nil {
			t.Fatal(err)
		}
		if c, _, err := CheckCompatibility(dt, dt); err != nil || c != CompatExact {
			t.Fatalf("opaque identity: %v %v", c, err)
		}
		if _, _, err := CheckCompatibility(dt, TypeEvent); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("opaque event conversion: %v", err)
		}
	}
	reg := NewRegistry()
	source := MustParseTypeID("test/source@1")
	sink := MustParseTypeID("test/sink@1")
	reg.MustRegister(TypeDef{ID: source, Outlets: []PortDef{{Name: "out", Type: TypeString}}})
	reg.MustRegister(TypeDef{ID: sink, Inlets: []PortDef{{Name: "in", Type: TypeBytes}}})
	sp, _ := NewSpec("policy")
	sp.Nodes["source"] = NodeSpec{Type: source}
	sp.Nodes["sink"] = NodeSpec{Type: sink}
	sp.Edges = []EdgeSpec{{From: MustParsePortRef("source.out"), To: MustParsePortRef("sink.in")}}
	if _, err := Validate(sp, reg, ValidateOptions{}); err != nil {
		t.Fatal(err)
	}
	backend := &BackendProfile{Compatibility: ExactCompatibility}
	if _, err := Validate(sp, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("host rejection ignored: %v", err)
	}
	backend.Compatibility = func(out, in DataType) (Compatibility, CoercionKind, error) { return CompatCoerce, "host/encode", nil }
	report, err := Validate(sp, reg, ValidateOptions{Backend: backend})
	if err != nil || report.Edges[0].Coercion != "host/encode" {
		t.Fatalf("host conversion lost: %+v %v", report, err)
	}
	backend.Compatibility = func(out, in DataType) (Compatibility, CoercionKind, error) {
		return CompatIncompatible, CoerceNone, nil
	}
	if _, err := Validate(sp, reg, ValidateOptions{Backend: backend}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("invalid policy result accepted: %v", err)
	}
}

func TestComponentBoundariesRequireExactTypes(t *testing.T) {
	reg := NewRegistry()
	tid := MustParseTypeID("test/bytes@1")
	reg.MustRegister(TypeDef{ID: tid, Inlets: []PortDef{{Name: "in", Type: TypeBytes}}, Outlets: []PortDef{{Name: "out", Type: TypeBytes}}})
	for _, outlet := range []bool{false, true} {
		comp, _ := NewSpec("wrapper")
		comp.Nodes["inner"] = NodeSpec{Type: tid}
		port := ExposedPortSpec{Name: "boundary", Type: TypeJSON, Target: MustParsePortRef("inner.in")}
		if outlet {
			port.Target = MustParsePortRef("inner.out")
			comp.Outlets = []ExposedPortSpec{port}
		} else {
			comp.Inlets = []ExposedPortSpec{port}
		}
		if _, err := SynthesizeTypeDef(comp, reg, ValidateOptions{}); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("coercing boundary accepted (outlet=%v): %v", outlet, err)
		}
		if outlet {
			comp.Outlets[0].Type = TypeBytes
		} else {
			comp.Inlets[0].Type = TypeBytes
		}
		lib := NewLibrary()
		componentID, err := lib.Put(comp, "test")
		if err != nil {
			t.Fatal(err)
		}
		root, _ := NewSpec("root")
		root.Nodes["instance"] = NodeSpec{Type: componentID}
		if _, _, err := Flatten(root, reg, lib, FlattenOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}
