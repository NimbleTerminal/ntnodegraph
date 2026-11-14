import { BoobaTerminal, installActionBar, installRendererHud, parseRendererFromURL } from './booba/booba.js';

const demos = {
  editor: 'Visual Graph Editor',
  subgraphs: 'Composable Subgraphs (graph/*)',
  mcp: 'Live MCP Co-Editing & Canonical YAML',
};

const help = {
  editor: 'Drag nodes or wire ports · wheel/+/-: zoom · v: Vector/Grid · w: inline params · O: card previews · a: add node · i: inspector · L: auto-layout · u/U: undo/redo · q: quit.',
  subgraphs: 'Select synth/cinema + Enter (or double-click) to drill into graph/* · Backspace/Esc to return · click group header + G to promote · y: toggle Flatten() pane · q: quit.',
  mcp: 'Press p (or MCP Patch) to execute real ngmcp JSON-RPC 2.0 edit/component/validate calls · drag/edit on canvas to update canonical YAML live · F: Split/Full · [/]: scroll YAML · q: quit.',
};

const params = new URLSearchParams(location.search);
const requested = params.get('demo');
const demo = Object.hasOwn(demos, requested) ? requested : 'editor';
const modeParam = params.get('mode') === 'grid' ? 'grid' : null;
const navKey = demo === 'editor' && modeParam === 'grid' ? 'grid' : demo;
const title = navKey === 'grid' ? 'Unicode Cell-Grid Canvas' : demos[demo];

const status = document.querySelector('#status');
document.querySelector('#title').textContent = title;
document.title = `${title} · ntnodegraph`;

const restart = new URL(location.href);
restart.searchParams.set('demo', demo);
if (modeParam) {
  restart.searchParams.set('mode', modeParam);
} else {
  restart.searchParams.delete('mode');
}
document.querySelector('#restart').href = restart.href;
document.querySelector('#source').href = `https://github.com/NimbleTerminal/ntnodegraph/tree/main/examples/${demo}`;

for (const link of document.querySelectorAll('[data-demo]')) {
  if (link.dataset.demo === navKey) {
    link.setAttribute('aria-current', 'page');
  }
}
document.querySelector('#help').textContent = help[demo];

function failed(error) {
  status.dataset.error = '';
  status.textContent = `Could not run the demo: ${error.message}. Try Restart.`;
  console.error(error);
}

async function main() {
  const go = new Go();
  go.argv = modeParam ? [demo, `-mode=${modeParam}`] : [demo];

  const response = await fetch(`demos/${demo}.wasm`);
  if (!response.ok) {
    throw new Error(`Download failed (${response.status})`);
  }

  let instance;
  if (typeof WebAssembly.instantiateStreaming === 'function') {
    try {
      ({ instance } = await WebAssembly.instantiateStreaming(response.clone(), go.importObject));
    } catch {
      const bytes = await response.arrayBuffer();
      ({ instance } = await WebAssembly.instantiate(bytes, go.importObject));
    }
  } else {
    const bytes = await response.arrayBuffer();
    ({ instance } = await WebAssembly.instantiate(bytes, go.importObject));
  }

  const running = go.run(instance);
  running.then(() => {
    status.textContent = 'Session ended. Choose Restart or another example above to run again.';
  }, failed);

  const terminal = new BoobaTerminal('terminal', {
    renderer: parseRendererFromURL(),
    allowOSC7501Actions: true,
    theme: { background: '#0b0f19', foreground: '#e2e8f0' },
  });
  await terminal.init();

  terminal.attachCustomWheelEventHandler(event => {
    if (terminal.hasMouseTracking() && terminal.term?.inputHandler) {
      terminal.term.inputHandler.handleWheel(event);
      return true;
    }
    return false;
  });

  terminal.setActionKeyEncoder({
    canEncode: gesture => terminal.encodeKey(gesture) !== null,
    encode: gesture => terminal.encodeKey(gesture),
  });
  installActionBar(terminal.actions, {
    parent: document.querySelector('#controls'),
    openKeyboard: () => terminal.focus(),
    onLayout: () => terminal.fitAddon?.fit(),
  });
  terminal.onActionError = failed;
  window.addEventListener('pagehide', () => terminal.dispose(), { once: true });

  terminal.connectWasm();
  terminal.focus();

  installRendererHud(terminal.term, {
    parent: document.querySelector('.terminal-frame'),
    position: 'absolute',
    bindToggleHotkey: false,
  });

  status.textContent = 'Running 100% locally in your browser (pure Go js/wasm + Kitty/ntcanvas). No terminal server required.';
}

main().catch(failed);
