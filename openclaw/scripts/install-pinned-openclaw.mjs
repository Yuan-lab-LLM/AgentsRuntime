import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
const lock = JSON.parse(fs.readFileSync(new URL('../openclaw102.lock.json', import.meta.url), 'utf8'));
assert.equal(process.version, lock.nodeVersion, 'Node must match the tested release');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'clawmanager-core-'));
function npm(args) {
  const result = spawnSync('npm', args, { encoding: 'utf8', timeout: 600000, maxBuffer: 16 << 20 });
  assert.equal(result.status, 0, result.stderr || result.error?.message || `npm ${args[0]} failed`);
  return result.stdout;
}
try {
  const [packed] = JSON.parse(npm(['pack', `openclaw@${lock.version}`, '--ignore-scripts', '--json', '--pack-destination', dir]));
  assert.equal(packed.version, lock.version);
  assert.equal(packed.integrity, lock.integrity);
  assert.equal(path.basename(packed.filename), packed.filename);
  const tar = path.join(dir, packed.filename);
  assert.equal('sha512-' + crypto.createHash('sha512').update(fs.readFileSync(tar)).digest('base64'), lock.integrity);
  npm(['install', '--global', tar, '--ignore-scripts']);
  // npm 11 requires explicit native script approval; unrelated global packages
  // are never rebuilt. All five scripts are from the tested upstream install.
  npm(['rebuild', '--global', 'openclaw', '@google/genai', 'esbuild', 'koffi', 'protobufjs', '--allow-scripts=@google/genai,esbuild,koffi,protobufjs,openclaw']);
  assert.equal(JSON.parse(fs.readFileSync('/usr/local/lib/node_modules/openclaw/package.json')).version, lock.version);
  console.log(`Verified and installed OpenClaw ${lock.version}, ${lock.integrity}`);
} finally {
  fs.rmSync(dir, { recursive: true, force: true });
}
