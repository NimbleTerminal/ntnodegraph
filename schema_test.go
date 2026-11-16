// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileSpecSchema(t testing.TB) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(SchemaV1JSON()))
	if err != nil {
		t.Fatalf("UnmarshalJSON(SchemaV1JSON): %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(SchemaV1URI, doc); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	sch, err := c.Compile(SchemaV1URI)
	if err != nil {
		t.Fatalf("Compile schema: %v", err)
	}
	return sch
}

func TestJSONSchemaV1Compliance(t *testing.T) {
	sch := compileSpecSchema(t)

	// 1. Canonical HN spec validates against Draft 2020-12 JSON Schema.
	hnSpec, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	// Exercise reserved inlets/outlets and groups in schema validation as well.
	richSpec, err := ApplyEdits(
		hnSpec,
		PutGroup("main", GroupSpec{
			Label: "Main Pipeline",
			Nodes: []string{"hn", "typeset", "view"},
			UI:    &GroupUI{X: 10, Y: 10, Width: 700, Height: 240},
		}),
	)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	richSpec.ComponentVersion = 2
	richSpec.Inlets = []ExposedPortSpec{
		{
			Name:     "trig",
			Type:     TypeEvent,
			Targets:  []PortRef{MustParsePortRef("hn.trigger"), MustParsePortRef("typeset.reset")},
			Required: true,
		},
	}
	richSpec.Outlets = []ExposedPortSpec{
		{Name: "doc", Type: TypeDocument, Target: MustParsePortRef("typeset.document"), Description: "Rendered HN document"},
	}
	richSpec.Params = []ExposedParamSpec{
		{
			Name:        "endpoint",
			Type:        ParamString,
			Target:      MustParsePortRef("hn.url"),
			Default:     "https://hacker-news.firebaseio.com/v0/topstories.json",
			Description: "Upstream URL",
		},
		{
			Name:    "max_items",
			Type:    ParamInt,
			Targets: []PortRef{MustParsePortRef("typeset.limit"), MustParsePortRef("view.limit")},
			Default: int64(25),
			Min:     FloatBound(1),
			Max:     FloatBound(100),
		},
	}

	for name, sp := range map[string]*Spec{"hn": hnSpec, "rich": richSpec} {
		rawJSON, err := MarshalJSON(sp)
		if err != nil {
			t.Fatalf("%s MarshalJSON: %v", name, err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawJSON))
		if err != nil {
			t.Fatalf("%s UnmarshalJSON: %v", name, err)
		}
		if err := sch.Validate(inst); err != nil {
			t.Fatalf("%s failed JSON Schema v1 validation: %v\nJSON:\n%s", name, err, rawJSON)
		}
	}

	// 2. Invalid JSON documents must fail JSON Schema validation.
	invalidDocs := map[string]string{
		"wrong_version":         `{"version":2,"name":"bad","nodes":{},"edges":[]}`,
		"extra_top_field":       `{"version":1,"name":"bad","eval":"1+1","nodes":{},"edges":[]}`,
		"bad_type_id":           `{"version":1,"name":"bad","nodes":{"n":{"type":"not-a-type-id"}},"edges":[]}`,
		"double_dot_type_id":    `{"version":1,"name":"bad","nodes":{"n":{"type":"source/a..b@1"}},"edges":[]}`,
		"c1_control_capability": `{"version":1,"name":"bad","nodes":{"n":{"type":"source/http@1","capabilities":["net:example.com\u0085"]}},"edges":[]}`,
		"bad_port_ref":          `{"version":1,"name":"bad","nodes":{},"edges":[{"from":"onlynode","to":"a.b"}]}`,
		"bad_data_type":         `{"version":1,"name":"bad","inlets":[{"name":"in","type":"any<bad>","target":"a.b"}],"nodes":{},"edges":[]}`,
		"zero_component_ver":    `{"version":1,"name":"bad","component_version":0,"nodes":{},"edges":[]}`,
		"inlet_both_targets":    `{"version":1,"name":"bad","inlets":[{"name":"in","type":"bytes","target":"a.b","targets":["a.b"]}],"nodes":{},"edges":[]}`,
		"outlet_multi_targets":  `{"version":1,"name":"bad","outlets":[{"name":"out","type":"bytes","targets":["a.b"]}],"nodes":{},"edges":[]}`,
		"bad_param_type":        `{"version":1,"name":"bad","params":[{"name":"p","type":"expr","target":"a.b"}],"nodes":{},"edges":[]}`,
	}
	for name, docStr := range invalidDocs {
		var inst any
		if err := json.Unmarshal([]byte(docStr), &inst); err != nil {
			t.Fatalf("%s invalid test json: %v", name, err)
		}
		if err := sch.Validate(inst); err == nil {
			t.Errorf("%s unexpectedly passed JSON Schema validation: %s", name, docStr)
		}
	}
}

func TestOpaqueTypeSchemaMatchesParser(t *testing.T) {
	sch := compileSpecSchema(t)
	for _, name := range []string{"audio/pcm", "finance/order.v2", "audio/a..b", "audio/pcm<stereo>", "audio/", "audio/a/b"} {
		_, parseErr := ParseDataType(name)
		doc := map[string]any{
			"version": 1, "name": "opaque", "nodes": map[string]any{}, "edges": []any{},
			"inlets": []any{map[string]any{"name": "in", "type": name, "target": "node.in"}},
		}
		if err := sch.Validate(doc); (err == nil) != (parseErr == nil) {
			t.Errorf("%q: schema=%v parser=%v", name, err, parseErr)
		}
	}
}
