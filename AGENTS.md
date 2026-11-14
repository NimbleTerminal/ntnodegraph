# AGENTS.md

Project rules and architectural invariants for agents and humans working on
`ntnodegraph` (`nimbleterminal.dev/ntnodegraph`).

## Core Principles

1. **The spec is a document, never a programming language.**
   `ntnodegraph` specs declare topology (`nodes`, `edges`), static `params`,
   host `capabilities`, optional subgraph boundary ports/parameters (`inlets`,
   `outlets`, `params`), and editor-only `ui` / `groups` metadata. Never add
   loops, conditionals, expressions, interpolation, or scripting to the spec.
   Push all computation into host runtime or WebAssembly node implementations.
2. **Strict backend-neutrality and zero host I/O.**
   `ntnodegraph`, `ntnodegraph/editor`, and `ntnodegraph/ngmcp` know nothing
   about Nebula, clocks, schedulers, or host I/O (`os`, `net`, `net/http`,
   `os/exec`, `syscall`, `plugin`, `time/tzdata`, `unsafe`). Execution
   runtimes inject their constraints via `BackendProfile` and `DocumentStore`.
   `TestExtractionBoundaries`, `TestEditorExtractionBoundaries`, and
   `TestNgmcpExtractionBoundaries` enforce these boundaries at test time.
3. **MIT licensing.**
   Every Go file in `ntnodegraph` is MIT-licensed (`LICENSE`) and must carry
   `// SPDX-License-Identifier: MIT`.
4. **Bounded inputs and strict codecs.**
   Parsers enforce bounded bytes, node counts, edge counts, group counts, and
   nesting depth (`ParseLimits`) and reject unknown fields, YAML aliases,
   multi-document streams, non-finite numbers, and duplicate JSON keys.
5. **Pure Go & WASM portability.**
   `ntnodegraph` builds and tests with `GOWORK=off` and `CGO_ENABLED=0`, and
   must pass both native `-race` tests and `GOOS=js GOARCH=wasm` compile and
   Node execution gates (`task check`).
6. **Deterministic iteration and diff-stable output.**
   Never rely on Go map iteration order in serialization, validation error
   reporting, topological sorting, component synthesis, or structural diffing.
   Canonical YAML and JSON outputs must remain byte-for-byte deterministic so
   version control and LLM/MCP edits produce minimal diffs.
