# Changelog

All notable changes to `nimbleterminal.dev/ntnodegraph` are documented in this file.

## Unreleased

### Changed

- **Breaking:** `DocumentStore.Get` now returns a revision; `Put` requires
  `ExpectedRevision` and rejects stale writes with `ErrConflict`. Custom stores
  must implement atomic compare-and-swap. The MCP demo tracks editor revisions.
- **Breaking:** component boundary ports require exact type matches. Move
  boundary conversions onto ordinary edges or explicit conversion nodes.
- **Breaking:** removed `SynthesizeExecutionHints`; component scheduling analysis
  belongs in host integrations. `ngmcp.TypeAnnotation` is now a namespaced map
  returned as `annotations`, replacing `frame_bound` and inlet `hot` fields.
- Added namespaced opaque port types and `BackendProfile.Compatibility`, used
  consistently by validation, flattening, editor wiring, and MCP splice inference.
  The default retains standard conversions; `ExactCompatibility` disables them.
- MemoryStore chooses the lexically first initial target when no default is given.
- Expanded CI fuzzing to eleven targets, adding parser/schema parity, structured
  extraction/flattening identity, and stateful revision checks. MCP patch fuzzing
  now checks input immutability, serialization round trips, and determinism.

## [0.1.0] - 2026-11-14

### Added

- **`ntnodegraph` (root package)**: Extracted standalone MIT-licensed declarative node-graph specification v1, conservative port `DataType` and coercion rules, fail-closed `Capability.Covers` matching (`net:` subdomain wildcards and `fs:` path-cleaned scopes), `Registry` / `TypeDef` / `BackendProfile`, deterministic `Validate` (with `BackendProfile.CycleBreakingEdgeKinds` and optional per-edge `BackendProfile.CycleBreakingEdge` predicates), diff-stable `ParseYAML` / `MarshalYAML` / `ParseJSON` / `MarshalJSON` codecs, `EditOp` / `ApplyEdits`, `DiffSpecs`, composable `graph/*` `Library` / `SynthesizeTypeDef` / `Flatten` / `ExtractSubgraph` / `PromoteGroupToSubgraph`, `NamespaceAgent` (`"agent"`), and embedded JSON Schema Draft 2020-12 (`schema/v1.schema.json`).
- **`ntnodegraph/editor`**: Extracted standalone Bubble Tea v2 node-and-wire visual editor (`RenderVector` and `RenderGrid`), interactive port wiring, inline parameter drawers, undo/redo history, group-to-subgraph promotion, breadcrumb component drill-in, and `NamespaceAgent` theme palette support.
- **`ntnodegraph/ngmcp`**: Extracted standalone transport-agnostic MCP JSON-RPC 2.0 authoring server (`list`, `read`, `validate`, `edit`, `component`) and `DocumentStore` / `MemoryStore`.
- **Examples, CI/CD & GitHub Pages showcase**: Added standalone `examples/` module (`editor`, `subgraphs`, `mcp`) with `booba` OSC 7501 action bars, interactive GitHub Pages browser showcase (`web/`), headless `ntwebterm` + `booba` Node smoke tests (`scripts/`), `THIRD_PARTY_NOTICES.md` generator (`task notices`), and GitHub Actions workflows (`.github/workflows/ci.yml`, `.github/workflows/pages.yml`).
