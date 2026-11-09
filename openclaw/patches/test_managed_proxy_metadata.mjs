// Actual supported metadata/model readers, isolated catalog facts, no model calls.
// Run in the Lite image with this patches directory mounted read-only.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { withManagedProxyPolicy } from './managed_proxy_policy.mjs';

const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const version = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8')).version;
if (version === '2026.7.1-2' && process.argv.includes('--allow-legacy')) {
  console.log('SKIP: native 7.1 unchanged; metadata identity contract is 8.1-specific');
  process.exit(0);
}
assert.ok(['2026.8.1','2026.10.2'].includes(version), `unsupported metadata contract ${version}`);
const dist = path.join(root, 'dist');
const load = async prefix => {
  const files = fs.readdirSync(dist).filter(file => file.startsWith(prefix) && /\.m?js$/.test(file));
  assert.equal(files.length, 1, `ambiguous native module: ${prefix}`);
  return import(pathToFileURL(path.join(dist, files[0])));
};
const { createGatewayChatMetadataRuntime } = await load('chat-metadata-runtime-');
// The hashed facade exports public names; do not reimplement native readers.
const modules = fs.readdirSync(dist).filter(file => file.startsWith('models-list-result-') && /\.m?js$/.test(file));
const facade = modules.find(file => /export \{[^}]*\bprepareModelsListResult\b(?!\s+as\b)/.test(fs.readFileSync(path.join(dist, file), 'utf8')));
assert.ok(facade, 'native model reader exports changed');
const { prepareModelsListResult } = await import(pathToFileURL(path.join(dist, facade)));
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'managed-metadata-'));
try {
  const file = path.join(dir, 'policy.json');
  const env = { CLAWMANAGER_PROXY_POLICY_FILE: file, CLAWMANAGER_PROXY_POLICY_NAMESPACE: 'isolated-test' };
  const now = 100000;
  const publish = proxies => fs.writeFileSync(file, JSON.stringify({ contract: 1, namespace: 'isolated-test', proxies, expires_at_ms: now + 60000 }));
  publish(['10.0.0.1']);
  const cfg = { agents: { defaults: { workspace: dir, model: { primary: 'openai/test' } } }, gateway: { trustedProxies: ['10.0.0.1'], allowRealIpFallback: false } };
  const snapshot = { entries: [], routeVariants: [] };
  const authStore = { version: 1, profiles: {} };
  const metadataSnapshot = { plugins: [], index: { plugins: [] }, owners: { providers: new Map(), modelIdNormalizationPolicies: new Map() } };
  const owner = { agentId: 'main', agentDir: path.join(dir, 'agent'), workspaceDir: dir, config: cfg, modelCatalog: snapshot, metadataSnapshot, authStore, authModes: {}, isCurrent: () => true };
  for (const mode of ['unmanaged', 'old-clone-reproduction', 'managed']) {
    let builds = 0;
    const getConfig = () => mode === 'unmanaged' ? cfg : mode === 'old-clone-reproduction'
      ? { ...cfg, gateway: { ...cfg.gateway } }
      : withManagedProxyPolicy(cfg, env, now);
    const context = { getRuntimeConfig: getConfig, logGateway: { debug() {}, warn() {} } };
    const runtime = createGatewayChatMetadataRuntime({
      getConfig, getContext: () => context, log: { warn() {} }, refreshOnRead: true,
      deps: {
        getPreparedOwner: () => owner, getPreparedAuthStore: () => authStore,
        getAuthStoreRevision: () => 0, getSkillsVersion: () => 0, getPluginRegistryVersion: () => 0,
        buildCommands: async () => ({ commands: [] }),
        buildProjection: async () => {
          // Only the diagnostic breaks the faulty loop. Production is unchanged.
          if (++builds > 50) throw new Error('DIAGNOSTIC_NO_PROGRESS_LIMIT');
          const reader = await prepareModelsListResult({
            ...(version === '2026.10.2' ? { source: { kind: 'gateway', context } } : {}),
            context, agentId: 'main', params: { view: 'configured' }, preloadedOnly: true,
            preloadedCatalog: { agentId: 'main', config: cfg, snapshot },
            catalogProjector: { metadataSnapshot, authStore, snapshot, isCurrent: () => true, prepareSelectedAccountCatalog: async () => {}, evaluateNative: (entry, host) => host, evaluateEntry: async () => ({}) },
          });
          return { isCurrent: reader.isCurrent, read: () => ({ modelCatalog: [], models: reader.read().models }) };
        },
      },
    });
    await runtime.refresh();
    if (mode === 'old-clone-reproduction') {
      await assert.rejects(runtime.read({ agentId: 'main' }), /DIAGNOSTIC_NO_PROGRESS_LIMIT/);
      assert.equal(builds, 51);
    } else {
      const result = await runtime.read({ agentId: 'main' });
      assert.deepEqual(result.models, []);
      await runtime.read({ agentId: 'main' });
      assert.equal(builds, 1, 'unchanged native generation must not rebuild on every read');
    }
    console.log(`PASS: ${mode}, projection builds=${builds}`);
  }
} finally {
  fs.rmSync(dir, { recursive: true, force: true });
}
