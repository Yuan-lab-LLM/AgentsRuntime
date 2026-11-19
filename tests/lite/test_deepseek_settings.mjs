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
    return { getSnapshot: () => value, set: next => { value = next; }, update: change => { value = structuredClone(value); change(value); }, subscribe: () => () => {} };
  } },
};
vm.runInNewContext(source, {
  structuredClone,
  window: { __ModuleLoader__: { load: entry => { plugin = entry.factory(name => {
    assert.ok(dependencies[name], `unexpected dependency ${name}`);
    return dependencies[name];
  }); } } },
});
const view = { writable: false, namespaces: [{ ns: 'llm-pi-ai', value: { providers: { auto: { models: [{ id: 'auto' }] } } } }] };
const settings = ctx => ctx.settingsScope ?? ctx.configForms;
for (const isLoopback of [false, true]) {
  let reads = 0;
  const ctx = {
    remote: { $host: { isLoopback }, $on: () => () => {}, settings: { describe: async () => { reads++; return { ok: true, value: view }; } } },
    on: () => () => {}, effect: callback => callback(),
  };
  plugin.apply(ctx);
  const mirror = settings(ctx).describe();
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
await settings(denied).describe().ensure();
assert.equal(settings(denied).describe().getSnapshot().error, 'unauthorized');
assert.equal(settings(denied).describe().getSnapshot().view, undefined);
let writes = 0;
const writeDenied = {
  remote: { $host: { isLoopback: false }, $on: () => () => {}, settings: {
    describe: async () => ({ ok: true, value: view }),
    mutate: async () => { writes++; return { ok: false, error: { message: 'read-only provider' } }; },
  } },
  on: () => () => {}, effect: callback => callback(),
};
plugin.apply(writeDenied);
await settings(writeDenied).describe().ensure();
const scope = writeDenied.settingsScope
  ? writeDenied.settingsScope.bind({ namespace: 'llm-pi-ai', decode: value => value })
  : writeDenied.configForms.get('llm-pi-ai');
await scope.set('providers', { injected: {} });
assert.equal(writes, 1, 'settings writes must go through the host RPC');
assert.equal(settings(writeDenied).describe().getSnapshot().view, view, 'denied write must not change the mirror');
assert.equal(scope.getSnapshot().writable, false, 'provider write policy must remain authoritative');
assert.equal(settings(writeDenied).describe().getSnapshot().view.namespaces[0].value.providers.auto.models[0].id, 'auto');
assert.equal(writeDenied.remote.$host.isLoopback, false);
console.log('PASS: host settings mirror, denied reads/writes, provider policy, and unchanged host ownership');
