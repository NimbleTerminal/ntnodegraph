// Exercise one real ntnodegraph WASM demo through booba's JS bridge in Node.
// Verifies startup, OSC 7501 action snapshots, resize, state transitions, and clean exit.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { setTimeout as delay } from 'node:timers/promises';

const demo = process.argv[2];
const variant = process.argv[3];
assert.ok(['editor', 'subgraphs', 'mcp'].includes(demo), `unknown demo: ${demo}`);

vm.runInThisContext(await readFile('web/wasm_exec.js', 'utf8'));
const go = new Go();
if (demo === 'editor' && variant === 'grid') {
  go.argv = ['editor', '-mode=grid'];
} else {
  go.argv = [demo];
}

let exitCode;
go.exit = code => { exitCode = code; };

const { instance } = await WebAssembly.instantiate(await readFile(`web/demos/${demo}.wasm`), go.importObject);
const timeout = setTimeout(() => {
  console.error(`${demo}: timed out`);
  process.exit(1);
}, 20000);

const running = go.run(instance);
try {
  for (let n = 0; typeof globalThis.bubbletea_read !== 'function' && n < 120; n++) {
    await delay(20);
  }
  assert.equal(typeof globalThis.bubbletea_read, 'function', 'booba bridge installed');
  bubbletea_resize(112, 30);

  let output = '';
  for (let n = 0; n < 120 && !output.includes('\x1b]7501;?'); n++) {
    await delay(25);
    output += bubbletea_read();
  }
  assert.ok(output.length > 0, 'initial terminal view rendered');
  assert.ok(output.includes('\x1b]7501;?'), 'application queries OSC 7501 action support');

  bubbletea_write('\x1b]7501;?\x1b\\');
  let report;
  for (let n = 0; n < 120 && !report; n++) {
    await delay(25);
    output += bubbletea_read();
    report = output.match(/\x1b\]7501;state=idle:[^\x1b]*:msg=([A-Za-z0-9+/=]+)\x1b\\/);
  }
  assert.ok(report, 'application publishes OSC 7501 action snapshot');

  const payload = Buffer.from(report[1], 'base64').toString('utf8');
  const snapshot = JSON.parse(payload.split(' | booba-actions/v1 ')[1]);
  const quit = snapshot.actions.find(action => action.label === 'Quit');
  assert.ok(quit?.enabled, 'Quit action enabled');

  const checkAction = label => assert.ok(
    snapshot.actions.some(action => action.label === label && action.enabled),
    `${label} action`,
  );

  async function expectSummary(expected) {
    let received = '';
    for (let n = 0; n < 120; n++) {
      await delay(20);
      received += bubbletea_read();
      for (const match of received.matchAll(/\x1b\]7501;state=idle:[^\x1b]*:msg=([A-Za-z0-9+/=]+)\x1b\\/g)) {
        const message = Buffer.from(match[1], 'base64').toString('utf8');
        if (message.split(' | booba-actions/v1 ')[0] === expected) return;
      }
    }
    assert.fail(`Missing summary state: ${expected}; received ${JSON.stringify(received.slice(-600))}`);
  }

  async function send(bytes, summary) {
    bubbletea_read();
    bubbletea_write(bytes);
    await delay(30);
    bubbletea_write('\x1b]7501;?\x1b\\');
    await expectSummary(summary);
  }

  bubbletea_resize(108, 28);

  if (demo === 'editor') {
    checkAction('Vector/Grid');
    checkAction('Params');
    checkAction('Previews');
    checkAction('Add node');
    if (variant === 'grid') {
      await send('v', 'Editor: mode=vector nodes=8 edges=9 synced=true');
    } else {
      await send('v', 'Editor: mode=grid nodes=8 edges=9 synced=true');
      await send('v', 'Editor: mode=vector nodes=8 edges=9 synced=true');
    }
  } else if (demo === 'subgraphs') {
    checkAction('Drill in');
    checkAction('Promote group');
    checkAction('Flatten pane');
    await send('\r', 'Subgraphs: depth=1 libs=2 flat_nodes=10 pane=true mode=vector');
    await send('\x7f', 'Subgraphs: depth=0 libs=2 flat_nodes=10 pane=true mode=vector');
    await send('y', 'Subgraphs: depth=0 libs=2 flat_nodes=10 pane=false mode=vector');
  } else if (demo === 'mcp') {
    checkAction('MCP Patch');
    checkAction('Split/Full');
    await send('p', 'MCP: step=1 nodes=5 edges=4 layout=0 mode=vector');
    await send('p', 'MCP: step=2 nodes=4 edges=3 layout=0 mode=vector');
    await send('F', 'MCP: step=2 nodes=4 edges=3 layout=1 mode=vector');
  }

  bubbletea_write(quit.keys[quit.preferred].key);
  await running;
  assert.equal(exitCode, 0);
  assert.equal(globalThis.bubbletea_exited, true, 'exit published to adapter');
  const label = variant ? `${demo} (${variant})` : demo;
  console.log(`${label}: WASM startup, OSC 7501 actions, resize, state transitions, and clean exit passed.`);
} finally {
  clearTimeout(timeout);
}
process.exit(0);
