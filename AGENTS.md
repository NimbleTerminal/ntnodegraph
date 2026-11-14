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
3. **MIT licensing and third-party notices.**
   Every Go file in `ntnodegraph` and `examples/` is MIT-licensed (`LICENSE`)
   and must carry `// SPDX-License-Identifier: MIT`. Run `task notices` whenever
   dependencies or browser runtime assets change; `task check` verifies
   `THIRD_PARTY_NOTICES.md` is up to date.
4. **Separate `examples/` module.**
   Interactive terminal/browser demos (`examples/editor`, `examples/subgraphs`,
   `examples/mcp`) live in `examples/go.mod` so CLI flags and `booba` browser
   assets never pollute `nimbleterminal.dev/ntnodegraph`'s module graph.
5. **Bounded inputs and strict codecs.**
   Parsers enforce bounded bytes, node counts, edge counts, group counts, and
   nesting depth (`ParseLimits`) and reject unknown fields, YAML aliases,
   multi-document streams, non-finite numbers, and duplicate JSON keys.
6. **Pure Go & WASM portability.**
   `ntnodegraph` and `examples/` build and test with `GOWORK=off` and
   `CGO_ENABLED=0`, and must pass native `-race` tests, `GOOS=js GOARCH=wasm`
   Node execution tests, and headless `booba` + `ntwebterm` browser demo smoke
   checks (`task ci`).
7. **Deterministic iteration and diff-stable output.**
   Never rely on Go map iteration order in serialization, validation error
   reporting, topological sorting, component synthesis, or structural diffing.
   Canonical YAML and JSON outputs must remain byte-for-byte deterministic so
   version control and LLM/MCP edits produce minimal diffs.
