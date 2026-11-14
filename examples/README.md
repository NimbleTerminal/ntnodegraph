# ntnodegraph Examples

Standalone interactive Bubble Tea v2 examples for `nimbleterminal.dev/ntnodegraph`.
The examples live in a separate Go module (`nimbleterminal.dev/ntnodegraph/examples`)
so browser demo dependencies (`nimbleterminal.dev/booba`) never enter the core
library's dependency graph.

## Demos

- **`editor`** (`task run-editor`) — Visual graph editor showcasing multi-namespace
  nodes (`source/*`, `agent/*`, `transform/*`, `gpu/*`, `surface/*`), cold-inlet
  `agent/svg-coder@1` state feedback, antialiased `rasterx` + `ntpicture` Vector
  rendering (`v` toggles Unicode `ntcanvas` Grid mode), inline parameter drawers
  (`w`), and simulated on-card RGBA/text previews (`O`).
- **`subgraphs`** (`task run-subgraphs`) — Composable `graph/*` component library
  (`graph/svg-critic-loop@1` and `graph/cinema-grade@1`), `Enter` / `Backspace`
  breadcrumb drill-in, `G` group-to-subgraph promotion, and a live `Flatten()`
  side-pane (`y`) showing expanded `instance/child` IDs and rolled-up capabilities.
- **`mcp`** (`task run-mcp`) — Split-pane live co-editing between `editor.Model`
  and `ngmcp.Server` JSON-RPC 2.0 `tools/call` operations (`edit`, `component`,
  `validate`) over a shared `MemoryStore`, paired with live diff-stable canonical
  YAML output.

Pass `-mode=grid` to any demo to start directly in Unicode cell-grid mode:

```sh
task run-editor -- -mode=grid
```

## Browser WASM Site & Recording

```sh
task serve-wasm-site
# Open http://127.0.0.1:8000/web/

task record-demo
# Re-records examples/demo.tape -> docs/media/demo.gif via ntrecord (with Kitty graphics)
```
