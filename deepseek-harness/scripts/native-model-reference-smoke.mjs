// Run ONLY against a disposable pinned installation: installs the native patch.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { install } from './patch-model-reference.mjs';

const root = resolve(process.argv[2]);
// The images invoke the installer through a link to the native npm tree.
const linkDir = mkdtempSync(join(tmpdir(), 'dsh-image-link-'));
try {
  const linkedRoot = join(linkDir, 'dsh');
  symlinkSync(root, linkedRoot, 'dir');
  install(linkedRoot);
  install(linkedRoot, true);
} finally {
  rmSync(linkDir, { recursive: true, force: true });
}
const fromDSH = createRequire(join(root, 'package.json'));
const core = await import(pathToFileURL(fromDSH.resolve('@deepseek-ai/dsh-llm')));
const pi = await import(pathToFileURL(fromDSH.resolve('@deepseek-ai/dsh-llm-pi-ai')));
const controller = await import(pathToFileURL(fromDSH.resolve('@deepseek-ai/dsh-api-session-controller')));
assert.equal(typeof controller.SessionController, 'function');
const base = 'https://gateway.invalid/llm';
const key = 'disposable-test-token';
process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL = '1';
process.env.CLAWMANAGER_LLM_BASE_URL = base;
process.env.CLAWMANAGER_LLM_API_KEY = key;
const descriptor = { id: 'new-model', name: 'New model', provider: 'new-provider', inputModalities: ['text'], context: { contextWindow: 32000 } };
const profile = { baseURL: base, apiKeyEnv: 'CLAWMANAGER_LLM_API_KEY' };
const nativeProfile = await pi.PiAiAdapter.prototype.clawmanagerModelProfile.call({
  current: () => ({ profiles: new Map([['new-provider', profile]]), models: { getModels: () => [descriptor] } }),
  config: { resolveApiKey: async () => key },
}, 'new-provider');
assert.deepEqual(nativeProfile, { baseURL: base, apiKey: key, models: [descriptor] });
const runtime = Object.create(core.LlmRuntime.prototype);
Object.defineProperty(runtime, 'ctx', { value: { waterfall: (_self, _event, _options, next) => next() } });
let dispatches = 0;
const adapter = {
  clawmanagerModelProfile: async () => nativeProfile,
  resolveModel: async (provider, model) => { assert.equal(provider, 'new-provider'); assert.equal(model, 'new-model'); return descriptor; },
  listModels: async () => [descriptor],
  prepareCall: async (provider, model) => {
    assert.equal(provider, 'new-provider'); assert.equal(model, 'new-model');
    return { model: descriptor, stream: async function* (options) {
      assert.equal(options.model, 'new-model'); dispatches++;
      yield { type: 'finish', reason: { kind: 'stop' } };
    } };
  },
};
runtime.adapters = new Map([['new-provider', { provider: { id: 'new-provider' }, adapter }]]);
let items = [{ id: 'new-model', stable_id: 'deployment-1', aliases: ['old-model'], provider_refs: [{ provider_id: 'old-provider', model_id: 'old-model' }] }];
const originalFetch = globalThis.fetch;
globalThis.fetch = async (_url, options) => {
  assert.equal(options.headers.Authorization, `Bearer ${key}`);
  return Response.json({ models: items });
};
try {
  assert.equal((await runtime.resolveModelInfo('old-provider', 'old-model')).id, 'new-model');
  assert.equal((await runtime.listModels('new-provider')).length, 1);
  const prepared = await runtime.prepareCall(Object.freeze({ provider: 'old-provider', model: 'old-model' }));
  assert.equal(prepared.config.model, 'new-model');
  const chunks = [];
  for await (const chunk of prepared.stream({ ...prepared.config, messages: [] })) chunks.push(chunk);
  assert.equal(chunks[0].reason.kind, 'stop');
  assert.equal(dispatches, 1);
  const delayedPrepared = await runtime.prepareCall({ provider: 'new-provider', model: 'new-model' });
  items = [];
  const delayedChunks = [];
  for await (const chunk of delayedPrepared.stream({ ...delayedPrepared.config, messages: [] })) delayedChunks.push(chunk);
  assert.equal(delayedChunks[0].reason.failure.code, 'MODEL_UNAVAILABLE');
  assert.equal(dispatches, 1, 'revoked prepared call reached the upstream adapter');
  await assert.rejects(runtime.prepareCall({ provider: 'old-provider', model: 'old-model' }), { code: 'MODEL_UNAVAILABLE' });
  const revoked = [];
  for await (const chunk of runtime.stream({ provider: 'old-provider', model: 'old-model', messages: [] })) revoked.push(chunk);
  assert.equal(revoked[0].reason.kind, 'error');
  assert.equal(revoked[0].reason.failure.code, 'MODEL_UNAVAILABLE');
  assert.equal(dispatches, 1);
  assert.deepEqual(await runtime.listModels('new-provider'), []);
  console.log('Native Harness modules imported; restored dispatch, revoked dispatch and visible catalog passed');
} finally { globalThis.fetch = originalFetch; }
