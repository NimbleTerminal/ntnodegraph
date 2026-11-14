// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestExtractionBoundaries enforces that nimbleterminal.dev/ntnodegraph:
//  1. Never imports any Nebula runtime package (strict unidirectional dependency).
//  2. Never imports host I/O, networking, process, plugin, or unsafe packages
//     in non-test files (so the model and codecs stay pure, deterministic, and
//     js/wasm-portable).
//  3. Carries the MIT SPDX header on every Go source file.
func TestExtractionBoundaries(t *testing.T) {
	forbiddenImports := []string{
		"net",
		"net/http",
		"os",
		"os/exec",
		"plugin",
		"syscall",
		"time/tzdata",
		"unsafe",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	fset := token.NewFileSet()
	checkedFiles := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		checkedFiles++

		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if !strings.Contains(string(src), "SPDX-License-Identifier: MIT") {
			t.Errorf("%s is missing MIT SPDX license header", name)
		}

		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		fileAST, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		for _, imp := range fileAST.Imports {
			pkg, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", name, imp.Path.Value, err)
			}
			if strings.HasPrefix(pkg, "nimbleterminal.dev/nebula") {
				t.Errorf("%s imports Nebula package %q; ntnodegraph must never depend on Nebula", name, pkg)
			}
			if slices.Contains(forbiddenImports, pkg) {
				t.Errorf("%s imports forbidden host/system package %q", name, pkg)
			}
		}
	}

	if checkedFiles == 0 {
		t.Fatal("expected Go files in ntnodegraph")
	}
}
