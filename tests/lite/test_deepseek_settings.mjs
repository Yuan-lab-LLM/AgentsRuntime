// Exercise the actual pinned client plugin's remote settings mirror.
// Usage: node tests/lite/test_deepseek_settings.mjs <installed client.js>
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(process.argv[2], 'utf8');
let plugin;
class Service {
  constructor(ctx, name) { this.ctx = ctx; ctx[name] = this; }
}
const dependencies = {
  '@deepseek-ai/cordis': { Service },
  '@deepseek-ai/dsh-client-store': { createSnapshotStore(initial) {
    let value = initial;
    return { getSnapshot: () => value, set: next => { value = next; }, subscribe: () => () => {} };
  } },
};
vm.runInNewContext(source, {
  window: { __ModuleLoader__: { load: entry => { plugin = entry.factory(name => {
    assert.ok(dependencies[name], `unexpected dependency ${name}`);
    return dependencies[name];
  }); } } },
});
const view = { writable: false, namespaces: [{ ns: 'llm-pi-ai', value: { providers: { auto: { models: [{ id: 'auto' }] } } } }] };
for (const isLoopback of [false, true]) {
  let reads = 0;
  const ctx = {
    remote: { $host: { isLoopback }, $on: () => () => {}, settings: { describe: async () => { reads++; return { ok: true, value: view }; } } },
    on: () => () => {}, effect: callback => callback(),
  };
  plugin.apply(ctx);
  const mirror = ctx.settingsScope.describe();
  await mirror.ensure();
  assert.equal(reads, 1, `settings must reach the authenticated host when isLoopback=${isLoopback}`);
  assert.equal(mirror.getSnapshot().view, view);
  assert.equal(ctx.remote.$host.isLoopback, isLoopback, 'settings must not grant generic host ownership');
}
const denied = {
  remote: { $host: { isLoopback: false }, $on: () => () => {}, settings: { describe: async () => ({ ok: false, error: { message: 'unauthorized' } }) } },
  on: () => () => {}, effect: callback => callback(),
};
plugin.apply(denied);
await denied.settingsScope.describe().ensure();
assert.equal(denied.settingsScope.describe().getSnapshot().error, 'unauthorized');
assert.equal(denied.settingsScope.describe().getSnapshot().view, undefined);
console.log('PASS: remote and loopback settings read host; RPC denial retained; host ownership unchanged');
