import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { managedPresetPatches } from './prepare-managed-config.mjs';

test('managed overlay preserves custom preset fields and expressions, disables nested web tools', () => {
  class Expression { constructor(source) { this.source = source; } }
  const expression = new Expression('process.platform');
  const presets = [{ id: 'custom', name: '@deepseek-ai/dsh-agent-preset', config: {
    id: 'custom-mode', description: 'keep me', plugins: [
      { name: '@deepseek-ai/dsh-tool-web', config: { searchTimeoutMs: 3000 } },
      { name: 'cordis:group', config: [{ name: '@deepseek-ai/dsh-tool-web' }] },
      { name: 'custom-tool', disabled: expression },
    ],
  } }];
  const [patch] = managedPresetPatches(presets);
  assert.equal(patch.config.description, 'keep me');
  assert.equal(patch.config.plugins[0].disabled, true);
  assert.equal(patch.config.plugins[1].config[0].disabled, true);
  assert.equal(patch.config.plugins[2].disabled, expression);
  assert.throws(() => managedPresetPatches([]), /no agent presets/);
});

const source = readFileSync(new URL('../config/managed.cordis.patch.yml', import.meta.url), 'utf8');
const providerExpression = source.split('providers: !!js >-')[1].split('\n#')[0];
function providers(env) {
  return vm.runInNewContext(providerExpression, { process: { env: {
    CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON: JSON.stringify({ providers: [{ provider_id: 'managed', model_ids: ['auto', 'vision'] }], vision: { 'managed/vision': true } }),
    ...env,
  } } });
}
test('model metadata uses qualified, bare and fallback context limits', () => {
  const result = providers({ CLAWMANAGER_LLM_CONTEXT_WINDOW: '64000', CLAWMANAGER_LLM_CONTEXT_WINDOWS: '{"managed/auto":32000,"auto":99999,"vision":128000}' });
  assert.equal(result.managed.models[0].contextWindow, 32000);
  assert.equal(result.managed.models[1].contextWindow, 128000);
  assert.equal(result.managed.models[1].input.join(','), 'text,image');
  assert.equal(providers({}).managed.models[0].contextWindow, 128000);
  assert.throws(() => providers({ CLAWMANAGER_LLM_CONTEXT_WINDOWS: '{"auto":-1}' }), /Invalid model context/);
  assert.throws(() => providers({ CLAWMANAGER_LLM_CONTEXT_WINDOW: 'bad' }), /Invalid managed context/);
});

test('unknown ClawManager context limits keep all provider models available', () => {
  const unknown = providers({ CLAWMANAGER_LLM_CONTEXT_WINDOW: '0', CLAWMANAGER_LLM_CONTEXT_WINDOWS: '{"auto":0,"vision":0}' });
  assert.equal(unknown.managed.models.length, 2);
  assert.equal(unknown.managed.models[0].contextWindow, 128000);
  assert.equal(unknown.managed.models[1].contextWindow, 128000);
  const mixed = providers({ CLAWMANAGER_LLM_CONTEXT_WINDOW: '64000', CLAWMANAGER_LLM_CONTEXT_WINDOWS: '{"managed/auto":0,"auto":99999,"vision":32000}' });
  assert.equal(mixed.managed.models[0].contextWindow, 64000);
  assert.equal(mixed.managed.models[1].contextWindow, 32000);
  for (const value of [-1, 1.5, 'bad']) {
    assert.throws(() => providers({ CLAWMANAGER_LLM_CONTEXT_WINDOW: String(value) }), /Invalid managed context/);
    assert.throws(() => providers({ CLAWMANAGER_LLM_CONTEXT_WINDOWS: JSON.stringify({ auto: value }) }), /Invalid model context/);
  }
});
