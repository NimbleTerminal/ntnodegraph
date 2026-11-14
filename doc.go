// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Package ntnodegraph provides the declarative graph specification format,
// in-memory graph model, type system, node-type registry, validator, canonical
// JSON Schema, diff-stable YAML/JSON codecs, structural diffing, composable
// flatten-at-load subgraphs, and headless editing API for NimbleTerminal node
// graphs.
//
// An ntnodegraph Spec is strictly a document—never a programming language. It
// describes nodes, typed ports, edges, static parameters, UI layout metadata,
// subgraph boundary ports, and declared host capabilities. It contains no
// loops, conditionals, expressions, clocks, schedulers, or execution semantics.
// Runtimes such as Nebula consume validated specs and impose their own
// execution models via a BackendProfile.
package ntnodegraph
