// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

const hnBriefYAML = `version: 1
name: hn-frontpage
nodes:
  hn:
    type: source/http@1
    capabilities: [net:hacker-news.firebaseio.com]
    params: { url: "https://hacker-news.firebaseio.com/v0/topstories.json", poll: 60s }
    ui: { x: 40, y: 120 }
  typeset:
    type: wasm/neomantra.hn-typesetter@1
    params: { limit: 30 }
    ui: { x: 320, y: 120 }
  view:
    type: surface/nthtml@1
    ui: { x: 600, y: 120 }
edges:
  - from: hn.body
    to: typeset.json
  - from: typeset.document
    to: view.document
`

func TestYAMLJSONRoundTripAndDeterminism(t *testing.T) {
	specA, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML(hnBriefYAML): %v", err)
	}

	yamlA, err := MarshalYAML(specA)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	jsonA, err := MarshalJSON(specA)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// Construct an equivalent spec with reversed edge order and unsorted slices/maps.
	specB := &Spec{
		Version: 1,
		Name:    "hn-frontpage",
		Nodes: map[string]NodeSpec{
			"view": {
				Type: MustParseTypeID("surface/nthtml@1"),
				UI:   &NodeUI{X: 600, Y: 120},
			},
			"typeset": {
				Type:   MustParseTypeID("wasm/neomantra.hn-typesetter@1"),
				Params: map[string]any{"limit": 30},
				UI:     &NodeUI{X: 320, Y: 120},
			},
			"hn": {
				Type: MustParseTypeID("source/http@1"),
				Capabilities: []Capability{
					MustParseCapability("net:hacker-news.firebaseio.com"),
					MustParseCapability("net:hacker-news.firebaseio.com"), // duplicate compacted
				},
				Params: map[string]any{
					"poll": "60s",
					"url":  "https://hacker-news.firebaseio.com/v0/topstories.json",
				},
				UI: &NodeUI{X: 40, Y: 120},
			},
		},
		Edges: []EdgeSpec{
			{From: MustParsePortRef("typeset.document"), To: MustParsePortRef("view.document")},
			{From: MustParsePortRef("hn.body"), To: MustParsePortRef("typeset.json")},
		},
	}

	yamlB, err := MarshalYAML(specB)
	if err != nil {
		t.Fatalf("MarshalYAML(specB): %v", err)
	}
	jsonB, err := MarshalJSON(specB)
	if err != nil {
		t.Fatalf("MarshalJSON(specB): %v", err)
	}

	if !bytes.Equal(yamlA, yamlB) {
		t.Fatalf("non-deterministic YAML output:\n--- A ---\n%s\n--- B ---\n%s", yamlA, yamlB)
	}
	if !bytes.Equal(jsonA, jsonB) {
		t.Fatalf("non-deterministic JSON output:\n--- A ---\n%s\n--- B ---\n%s", jsonA, jsonB)
	}

	fromJSON, err := ParseJSON(jsonA)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if d := Diff(specA, fromJSON); !d.IsEmpty() {
		t.Fatalf("YAML -> JSON -> Spec round-trip diff not empty: %+v", d)
	}
}

func TestMinimalDiffOnEdgeAddition(t *testing.T) {
	before, err := ParseYAML([]byte(hnBriefYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	after, err := ApplyEdits(
		before,
		AddNode("audit", NodeSpec{
			Type: MustParseTypeID("surface/log@1"),
			UI:   &NodeUI{X: 600, Y: 260},
		}),
		Connect(MustParsePortRef("typeset.document"), MustParsePortRef("audit.in"), ""),
	)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	beforeYAML, err := MarshalYAML(before)
	if err != nil {
		t.Fatalf("MarshalYAML(before): %v", err)
	}
	afterYAML, err := MarshalYAML(after)
	if err != nil {
		t.Fatalf("MarshalYAML(after): %v", err)
	}

	// Every line of beforeYAML must still appear in order inside afterYAML:
	// adding a node and an edge never reshuffles unrelated nodes or edges.
	beforeLines := strings.Split(strings.TrimSpace(string(beforeYAML)), "\n")
	afterLines := strings.Split(strings.TrimSpace(string(afterYAML)), "\n")
	pos := 0
	for _, wantLine := range beforeLines {
		found := false
		for pos < len(afterLines) {
			if afterLines[pos] == wantLine {
				found = true
				pos++
				break
			}
			pos++
		}
		if !found {
			t.Fatalf("line %q from original YAML was reshuffled or lost:\n--- before ---\n%s\n--- after ---\n%s",
				wantLine, beforeYAML, afterYAML)
		}
	}
}

func TestStrictDecodeRejections(t *testing.T) {
	cases := []struct {
		name string
		in   string
		json bool
		want error
	}{
		{
			name: "unknown yaml field",
			in:   "version: 1\nname: bad\nscript: 'while(true){}'\nnodes: {}\nedges: []\n",
			want: ErrInvalidSpec,
		},
		{
			name: "yaml alias forbidden",
			in:   "version: 1\nname: bad\nnodes:\n  a: &anchor\n    type: source/timer@1\n  b: *anchor\nedges: []\n",
			want: ErrInvalidSpec,
		},
		{
			name: "yaml anchor without alias forbidden",
			in:   "version: 1\nname: bad\nnodes:\n  a:\n    type: &t source/timer@1\nedges: []\n",
			want: ErrInvalidSpec,
		},
		{
			name: "multiple yaml docs",
			in:   "version: 1\nname: one\nnodes: {}\nedges: []\n---\nversion: 1\nname: two\nnodes: {}\nedges: []\n",
			want: ErrInvalidSpec,
		},
		{
			name: "unsupported version",
			in:   `{"version":2,"name":"future","nodes":{},"edges":[]}`,
			json: true,
			want: ErrUnsupportedVersion,
		},
		{
			name: "duplicate json key",
			in:   `{"version":1,"name":"dup","name":"dup2","nodes":{},"edges":[]}`,
			json: true,
			want: ErrInvalidSpec,
		},
		{
			name: "unknown json field",
			in:   `{"version":1,"name":"extra","if":"x > 1","nodes":{},"edges":[]}`,
			json: true,
			want: ErrInvalidSpec,
		},
		{
			name: "trailing json token",
			in:   `{"version":1,"name":"trail","nodes":{},"edges":[]} {}`,
			json: true,
			want: ErrInvalidSpec,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.json {
				_, err = ParseJSON([]byte(tc.in))
			} else {
				_, err = ParseYAML([]byte(tc.in))
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err = %v, want %v", err, tc.want)
			}
		})
	}

	if _, err := ParseYAMLWithLimits([]byte(hnBriefYAML), ParseLimits{MaxBytes: 32}); !errors.Is(err, ErrSpecTooLarge) {
		t.Fatalf("expected ErrSpecTooLarge, got %v", err)
	}
}

func TestNumericParsingFidelity(t *testing.T) {
	// Reject non-decimal YAML integer notations and out-of-range int64 tokens.
	for _, badToken := range []string{
		"010",
		"01",
		"-01",
		"0x10",
		"0b10",
		"+10",
		"9223372036854775808",
		"9223372036854775809",
		"-9223372036854775809",
	} {
		yamlDoc := "version: 1\nname: num\nnodes:\n  n:\n    type: source/timer@1\n    params:\n      v: " + badToken + "\nedges: []\n"
		if _, err := ParseYAML([]byte(yamlDoc)); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ParseYAML(v=%s) got %v, want ErrInvalidSpec", badToken, err)
		}
	}

	for _, badJSON := range []string{
		"9223372036854775808",
		"9223372036854775809",
		"-9223372036854775809",
	} {
		jsonDoc := `{"version":1,"name":"num","nodes":{"n":{"type":"source/timer@1","params":{"v":` + badJSON + `}}},"edges":[]}`
		if _, err := ParseJSON([]byte(jsonDoc)); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ParseJSON(v=%s) got %v, want ErrInvalidSpec", badJSON, err)
		}
	}

	// Quoted "010" remains a string; exact int64 boundaries and >2^53 integers preserve precision;
	// scientific floats >= 2^63 round-trip across YAML and JSON cleanly.
	validYAML := `version: 1
name: fidelity
nodes:
  n:
    type: source/timer@1
    params:
      big_float: 1e+19
      exact_above_53b: 9007199254740993
      max_i64: 9223372036854775807
      min_i64: -9223372036854775808
      octal_str: "010"
edges: []
`
	sp, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML(validYAML): %v", err)
	}
	params := sp.Nodes["n"].Params
	if got, ok := params["octal_str"].(string); !ok || got != "010" {
		t.Fatalf("octal_str = %#v, want \"010\"", params["octal_str"])
	}
	if got, ok := params["exact_above_53b"].(int64); !ok || got != 9007199254740993 {
		t.Fatalf("exact_above_53b = %#v, want int64(9007199254740993)", params["exact_above_53b"])
	}
	if got, ok := params["max_i64"].(int64); !ok || got != math.MaxInt64 {
		t.Fatalf("max_i64 = %#v, want MaxInt64", params["max_i64"])
	}
	if got, ok := params["min_i64"].(int64); !ok || got != math.MinInt64 {
		t.Fatalf("min_i64 = %#v, want MinInt64", params["min_i64"])
	}

	js, err := MarshalJSON(sp)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	fromJS, err := ParseJSON(js)
	if err != nil {
		t.Fatalf("ParseJSON(MarshalJSON): %v\nJSON: %s", err, js)
	}
	if got := fromJS.Nodes["n"].Params["exact_above_53b"]; got != int64(9007199254740993) {
		t.Fatalf("JSON round-trip lost int64 precision: got %#v", got)
	}

	// uint64 above math.MaxInt64 must be rejected in programmatic edits and marshaling.
	if _, err := ApplyEdits(sp, SetParam("n", "overflow", uint64(math.MaxInt64)+1)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("SetParam(uint64 > MaxInt64) got %v, want ErrInvalidSpec", err)
	}
}

func TestMaxDepthCountsMapsAndSlicesUniformly(t *testing.T) {
	makeNestedMaps := func(levels int) any {
		var v any = "leaf"
		for range levels {
			v = map[string]any{"k": v}
		}
		return v
	}
	makeNestedSlices := func(levels int) any {
		var v any = "leaf"
		for range levels {
			v = []any{v}
		}
		return v
	}

	base, err := NewSpec("depth-check")
	if err != nil {
		t.Fatalf("NewSpec: %v", err)
	}
	base, err = ApplyEdits(base, AddNode("n", NodeSpec{Type: MustParseTypeID("source/timer@1")}))
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Depth == DefaultMaxDepth (32) must succeed for both maps and slices across Edit, YAML, and JSON.
	for name, nested := range map[string]any{
		"maps_32":   makeNestedMaps(DefaultMaxDepth),
		"slices_32": makeNestedSlices(DefaultMaxDepth),
	} {
		t.Run(name, func(t *testing.T) {
			edited, err := ApplyEdits(base, SetParam("n", "v", nested))
			if err != nil {
				t.Fatalf("SetParam(%s) unexpected error: %v", name, err)
			}
			yb, err := MarshalYAML(edited)
			if err != nil {
				t.Fatalf("MarshalYAML(%s): %v", name, err)
			}
			if _, err := ParseYAML(yb); err != nil {
				t.Fatalf("ParseYAML(%s): %v", name, err)
			}
			jb, err := MarshalJSON(edited)
			if err != nil {
				t.Fatalf("MarshalJSON(%s): %v", name, err)
			}
			if _, err := ParseJSON(jb); err != nil {
				t.Fatalf("ParseJSON(%s): %v", name, err)
			}
		})
	}

	// Depth == DefaultMaxDepth + 1 (33) must fail for both maps and slices across Edit, YAML, and JSON.
	for name, nested := range map[string]any{
		"maps_33":   makeNestedMaps(DefaultMaxDepth + 1),
		"slices_33": makeNestedSlices(DefaultMaxDepth + 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyEdits(base, SetParam("n", "v", nested))
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("SetParam(%s) got %v, want ErrInvalidSpec", name, err)
			}
			if !strings.Contains(err.Error(), "depth 33 exceeds max 32") {
				t.Fatalf("SetParam(%s) error %q did not report exact depth 33 exceeds max 32", name, err.Error())
			}
		})
	}

	// Also verify ParseYAMLWithLimits and ParseJSONWithLimits at custom MaxDepth: 4.
	sp4, err := ApplyEdits(base, SetParam("n", "v", makeNestedMaps(4)))
	if err != nil {
		t.Fatalf("ApplyEdits(maps_4): %v", err)
	}
	yb4, err := MarshalYAML(sp4)
	if err != nil {
		t.Fatalf("MarshalYAML(maps_4): %v", err)
	}
	jb4, err := MarshalJSON(sp4)
	if err != nil {
		t.Fatalf("MarshalJSON(maps_4): %v", err)
	}
	if _, err := ParseYAMLWithLimits(yb4, ParseLimits{MaxDepth: 4}); err != nil {
		t.Fatalf("ParseYAMLWithLimits(MaxDepth=4) failed at depth 4: %v", err)
	}
	if _, err := ParseYAMLWithLimits(yb4, ParseLimits{MaxDepth: 3}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("ParseYAMLWithLimits(MaxDepth=3) got %v, want ErrInvalidSpec", err)
	}
	if _, err := ParseJSONWithLimits(jb4, ParseLimits{MaxDepth: 4}); err != nil {
		t.Fatalf("ParseJSONWithLimits(MaxDepth=4) failed at depth 4: %v", err)
	}
	if _, err := ParseJSONWithLimits(jb4, ParseLimits{MaxDepth: 3}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("ParseJSONWithLimits(MaxDepth=3) got %v, want ErrInvalidSpec", err)
	}
}
