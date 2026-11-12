import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';

// Installed beside the locked package.json, never loaded from a workspace.
const root = path.dirname(fileURLToPath(import.meta.url));
const lock = JSON.parse(fs.readFileSync(path.join(root, 'package-lock.json')));
const source = JSON.parse(fs.readFileSync(path.join(root, 'source-lock.json')));
const packages = {};
for (const name of ['@deepseek-ai/dsh', 'pnpm', '@deepseek-ai/cordis', '@deepseek-ai/dsh-hmr', '@deepseek-ai/cordis-plugin-loader']) {
  const installed = JSON.parse(fs.readFileSync(path.join(root, 'node_modules', name, 'package.json')));
  assert.equal(installed.version, lock.packages['node_modules/' + name].version, 'Native dependency drift: ' + name);
  packages[name] = installed.version;
}
assert.equal(packages['@deepseek-ai/dsh'], source.dshVersion);
assert.equal(lock.packages['node_modules/@deepseek-ai/dsh'].integrity, source.dshIntegrity);
assert.equal(process.versions.node.split('.')[0], '24');
const sandbox = fs.readFileSync(path.join(root, 'node_modules/@deepseek-ai/dsh-sandbox-local/lib/index.js'));
const sandboxSHA256 = createHash('sha256').update(sandbox).digest('hex');
assert.equal(sandboxSHA256, process.argv.includes('--patched') ? source.sandboxPatchedSHA256 : source.sandboxOriginalSHA256);
const capabilityFile = path.join(root, 'adapter/deepseek-harness/team/capability.json');
const capability = fs.existsSync(capabilityFile) ? JSON.parse(fs.readFileSync(capabilityFile)) : null;
if (capability) {
  assert.equal(capability.native, packages['@deepseek-ai/dsh']);
  assert.equal(capability.protocol, 'dsh-team-v1');
  for (const file of ['plugin.mjs','host.mjs','protocol.mjs']) assert.ok(fs.statSync(path.join(path.dirname(capabilityFile), file)).isFile());
}
const report = { node: process.versions.node, packages, sandboxSHA256,
  dependencyLockSHA256: createHash('sha256').update(fs.readFileSync(path.join(root, 'package-lock.json'))).digest('hex'),
  teamEnabled: Boolean(capability), ...(capability ? { teamProtocol: capability.protocol } : {}) };
const output = process.argv[2];
if (output && !output.startsWith('--')) fs.writeFileSync(output, JSON.stringify(report, null, 2) + '\n');
else console.log(JSON.stringify(report));
