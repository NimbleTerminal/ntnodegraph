// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"errors"
	"testing"
)

func TestParseTypeID(t *testing.T) {
	valid := []struct {
		in   string
		want TypeID
	}{
		{"source/http@1", TypeID{Namespace: "source", Name: "http", Major: 1}},
		{"wasm/neomantra.hn-typesetter@1", TypeID{Namespace: "wasm", Name: "neomantra.hn-typesetter", Major: 1}},
		{"transform/duckdb-sql@12", TypeID{Namespace: "transform", Name: "duckdb-sql", Major: 12}},
		{"graph/ reusable_patch@1"[0:0] + "graph/reusable_patch@1", TypeID{Namespace: "graph", Name: "reusable_patch", Major: 1}},
	}
	for _, tc := range valid {
		got, err := ParseTypeID(tc.in)
		if err != nil {
			t.Fatalf("ParseTypeID(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseTypeID(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.in {
			t.Fatalf("TypeID.String() = %q, want %q", got.String(), tc.in)
		}
	}

	invalid := []string{
		"",
		"http@1",
		"source/http",
		"source/http@0",
		"source/http@01",
		"source/http@-1",
		"Source/http@1",
		"source/HTTP@1",
		"source/.hidden@1",
		"source/trailing.@1",
		"source/a..b@1",
		"source/a/b@1",
		"source/a@1@2",
	}
	for _, in := range invalid {
		if got, err := ParseTypeID(in); err == nil {
			t.Errorf("ParseTypeID(%q) succeeded with %+v, want error", in, got)
		} else if !errors.Is(err, ErrInvalidTypeID) {
			t.Errorf("ParseTypeID(%q) error = %v, want ErrInvalidTypeID", in, err)
		}
	}
}

func TestParsePortRef(t *testing.T) {
	ref, err := ParsePortRef("hn.body")
	if err != nil {
		t.Fatalf("ParsePortRef: %v", err)
	}
	if ref.Node != "hn" || ref.Port != "body" || ref.String() != "hn.body" {
		t.Fatalf("unexpected PortRef: %+v", ref)
	}

	for _, bad := range []string{"", "hn", ".body", "hn.", "hn.a.b", "-bad.port", "node.-bad"} {
		if _, err := ParsePortRef(bad); !errors.Is(err, ErrInvalidPortRef) {
			t.Errorf("ParsePortRef(%q) err = %v, want ErrInvalidPortRef", bad, err)
		}
	}
}

func TestParseDataTypeAndCompatibility(t *testing.T) {
	for _, valid := range []string{
		"bytes", "string", "number", "bool", "json",
		"table", "table<arrow>", "table<duckdb>",
		"document", "frame", "frame<rgba8>",
		"texture", "texture<rgba16f>",
		"event", "event<string>", "any",
	} {
		dt, err := ParseDataType(valid)
		if err != nil {
			t.Fatalf("ParseDataType(%q) unexpected error: %v", valid, err)
		}
		if dt.String() != valid {
			t.Fatalf("DataType.String() = %q, want %q", dt.String(), valid)
		}
	}

	for _, bad := range []string{
		"", "float64", "any<rgba8>", "number<int>", "bytes<utf8>",
		"frame<>", "frame<RGBA8>", "frame<rgba8", "frame>rgba8<", "frame<a<b>>",
	} {
		if _, err := ParseDataType(bad); !errors.Is(err, ErrInvalidDataType) {
			t.Errorf("ParseDataType(%q) err = %v, want ErrInvalidDataType", bad, err)
		}
	}

	allowed := []struct {
		out, in    string
		wantCompat Compatibility
		wantCoerce CoercionKind
	}{
		{"document", "document", CompatExact, CoerceNone},
		{"frame<rgba8>", "frame<rgba8>", CompatExact, CoerceNone},
		{"document", "any", CompatWildcard, CoerceNone},
		{"frame<rgba8>", "any", CompatWildcard, CoerceNone},
		{"frame<rgba8>", "frame", CompatWildcard, CoerceNone},
		{"texture<rgba8>", "texture", CompatWildcard, CoerceNone},
		{"event<number>", "event", CompatWildcard, CoerceNone},
		{"bytes", "json", CompatCoerce, CoerceJSONParse},
		{"string", "bytes", CompatCoerce, CoerceUTF8Bytes},
		{"bytes", "string", CompatCoerce, CoerceBytesUTF8},
		{"json", "bytes", CompatCoerce, CoerceJSONBytes},
		{"json", "string", CompatCoerce, CoerceJSONString},
		{"number", "json", CompatCoerce, CoerceJSONEncode},
		{"bool", "json", CompatCoerce, CoerceJSONEncode},
		{"string", "json", CompatCoerce, CoerceJSONEncode},
		{"document", "event", CompatCoerce, CoerceEventTrigger},
		{"number", "event", CompatCoerce, CoerceEventTrigger},
		{"texture<rgba8>", "frame<rgba8>", CompatCoerce, CoerceTextureDownload},
		{"texture<rgba8>", "frame", CompatCoerce, CoerceTextureDownload},
		{"frame<rgba8>", "texture<rgba8>", CompatCoerce, CoerceFrameUpload},
		{"frame", "texture", CompatCoerce, CoerceFrameUpload},
	}
	for _, tc := range allowed {
		compat, coerce, err := CheckCompatibility(MustParseDataType(tc.out), MustParseDataType(tc.in))
		if err != nil {
			t.Errorf("CheckCompatibility(%s -> %s) unexpected err: %v", tc.out, tc.in, err)
			continue
		}
		if compat != tc.wantCompat || coerce != tc.wantCoerce {
			t.Errorf("CheckCompatibility(%s -> %s) = (%v, %q), want (%v, %q)",
				tc.out, tc.in, compat, coerce, tc.wantCompat, tc.wantCoerce)
		}
	}

	rejected := [][2]string{
		{"document", "number"}, // explicit nonsense example from brief
		{"any", "document"},
		{"any", "event"},
		{"frame", "frame<rgba8>"},
		{"frame<bgra8>", "frame<rgba8>"},
		{"texture", "frame<rgba8>"},
		{"texture<rgba16f>", "frame<rgba8>"},
		{"number", "bool"},
		{"bool", "number"},
		{"string", "number"},
		{"number", "string"},
		{"event", "bool"},
		{"table", "json"},
		{"texture", "bytes"},
		{"frame<rgba8>", "json"},
	}
	for _, pair := range rejected {
		compat, _, err := CheckCompatibility(MustParseDataType(pair[0]), MustParseDataType(pair[1]))
		if !errors.Is(err, ErrTypeMismatch) || compat != CompatIncompatible {
			t.Errorf("CheckCompatibility(%s -> %s) = (%v, %v), want ErrTypeMismatch", pair[0], pair[1], compat, err)
		}
	}
}

func TestCapabilityCovers(t *testing.T) {
	exact := MustParseCapability("net:hacker-news.firebaseio.com")
	if !exact.Covers(MustParseCapability("net:HACKER-NEWS.firebaseio.com")) {
		t.Fatal("expected case-insensitive host match for net capability")
	}
	wildcard := MustParseCapability("net:*.firebaseio.com")
	if !wildcard.Covers(exact) {
		t.Fatal("expected *.firebaseio.com to cover hacker-news.firebaseio.com")
	}
	if wildcard.Covers(MustParseCapability("net:firebaseio.com")) {
		t.Fatal("*.firebaseio.com must not cover bare parent domain firebaseio.com")
	}
	fsDir := MustParseCapability("fs:/var/data/*")
	if !fsDir.Covers(MustParseCapability("fs:/var/data/ticks.parquet")) {
		t.Fatal("expected fs prefix wildcard to cover child file")
	}
	if !fsDir.Covers(MustParseCapability("fs:/var/data/sub/../ticks.parquet")) {
		t.Fatal("expected normalized subpath inside /var/data to be covered")
	}
	for _, escape := range []string{
		"fs:/var/data",
		"fs:/var/data/.",
		"fs:/var/data/../secrets",
		"fs:/var/data/../../etc/passwd",
		"fs:/var/data/sub/../../secrets",
		`fs:/var/data\..\secrets`,
	} {
		if fsDir.Covers(MustParseCapability(escape)) {
			t.Errorf("fs:/var/data/* must not cover %q", escape)
		}
	}

	relDir := MustParseCapability("fs:data/*")
	if !relDir.Covers(MustParseCapability("fs:data/sub/file.csv")) {
		t.Fatal("expected relative fs wildcard to cover child path")
	}
	for _, relEscape := range []string{
		"fs:data",
		"fs:data/.",
		"fs:data/../secrets",
		"fs:data/../../etc/passwd",
		"fs:/var/data/file.csv",
	} {
		if relDir.Covers(MustParseCapability(relEscape)) {
			t.Errorf("fs:data/* must not cover %q", relEscape)
		}
	}

	rootWildcard := MustParseCapability("fs:/*")
	if !rootWildcard.Covers(MustParseCapability("fs:/var/data/ticks.parquet")) {
		t.Fatal("expected fs:/* to cover absolute file")
	}
	if rootWildcard.Covers(MustParseCapability("fs:/")) {
		t.Fatal("fs:/* must not cover bare root /")
	}
	if MustParseCapability("fs:../escape/*").Covers(MustParseCapability("fs:../escape/a")) {
		t.Fatal("grant containing .. must never cover anything")
	}

	for _, bad := range []string{"", "NET:example.com", "net:", "net:bad host", "net:bad\nline"} {
		if _, err := ParseCapability(bad); !errors.Is(err, ErrInvalidCapability) {
			t.Errorf("ParseCapability(%q) = %v, want ErrInvalidCapability", bad, err)
		}
	}
}
