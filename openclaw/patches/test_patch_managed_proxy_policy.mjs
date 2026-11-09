import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'proxy-patch-contract-'));
const dist = path.join(dir, 'dist'); fs.mkdirSync(dist);
const file = path.join(dist, 'io.runtime-test.js');
const startup = path.join(dist, 'server-runtime-config-test.js');
const auth = path.join(dist, 'server-start-test.js');
fs.writeFileSync(path.join(dist, 'server-shared-auth-generation-test.js'), 'export { captureSharedGatewaySessionGenerationOwnership as t, replaceOwnedSharedGatewaySessionGenerationState as l, disconnectStaleSharedGatewayAuthClients as i };');
fs.writeFileSync(auth, 'import { s as getRequiredSharedGatewaySessionGeneration } from "./server-shared-auth-generation-test.js";\nconst generation = resolveSharedGatewaySessionGeneration(resolveGatewayAuth({}), config.gateway?.trustedProxies);\nconst options = { getRequiredSharedGatewaySessionGeneration: () => getRequiredSharedGatewaySessionGeneration(sharedGatewaySessionGenerationState), };');
fs.writeFileSync(startup, 'async function resolveGatewayRuntimeConfig(params) {\nreturn params.cfg;\n}');
const original = 'function getRuntimeConfig(options) {\n\treturn loadConfig(options);\n}';
const version = v => fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({version:v}));
const run = (...args) => spawnSync(process.execPath, [fileURLToPath(new URL('./patch_managed_proxy_policy.mjs', import.meta.url)), ...args], {
  env: {...process.env, OPENCLAW_PACKAGE_ROOT:dir}, encoding:'utf8',
});
try {
  version('2026.7.1-2'); fs.writeFileSync(file, original);
  assert.equal(run('--patch','--allow-legacy').status,0);
  assert.equal(fs.readFileSync(file,'utf8'),original);
  version('2026.8.2'); assert.notEqual(run('--patch','--allow-legacy').status,0);
  version('2026.8.1'); assert.equal(run('--patch').status,0);
  const patched=fs.readFileSync(file,'utf8');
  assert.equal(run('--patch').status,0); assert.equal(fs.readFileSync(file,'utf8'),patched);
  assert.equal(run().status,0);
  assert.match(fs.readFileSync(startup,'utf8'), /cfg: clawmanagerManagedStartupPolicy\(params.cfg\)/);
  assert.match(fs.readFileSync(auth,'utf8'), /CLAWMANAGER_MANAGED_SHARED_AUTH_V1/);
  assert.match(fs.readFileSync(auth,'utf8'), /clawmanagerAuthProxyPolicy\(config\)/);
  fs.writeFileSync(path.join(dist,'clawmanager-managed-proxy-policy.mjs'),'invalid helper');
  assert.notEqual(run().status,0); assert.equal(run('--patch').status,0);
  fs.writeFileSync(file,patched.replace('const config = loadConfig(options);','const config = unexpected();'));
  assert.notEqual(run('--patch').status,0);
  console.log('PASS: 7.1 unchanged, unknown version rejected, idempotent 8.1 patch and integrity checks');
} finally { fs.rmSync(dir,{recursive:true,force:true}); }
