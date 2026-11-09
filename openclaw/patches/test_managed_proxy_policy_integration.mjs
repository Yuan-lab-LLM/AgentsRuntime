// Run inside the candidate image only: no live credentials, models or workspace.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';

const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const require = createRequire(path.join(root, 'package.json'));
const WebSocket = require('ws');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'proxy-contract-'));
const config = path.join(dir, 'openclaw.json');
const policy = path.join(dir, 'policy.json');
const port = 19871;
const token = 'isolated-integration-test-token-not-production';
fs.writeFileSync(config, JSON.stringify({
  gateway: { mode: 'local', bind: 'loopback', port, auth: { mode: 'token', token },
    trustedProxies: ['127.0.0.0/8', '10.244.0.0/16'],
    http: { endpoints: { chatCompletions: { enabled: true } } } },
  plugins: { enabled: false }, browser: { enabled: false },
}));
function publish(proxies, expires = Date.now() + 60000) {
  fs.writeFileSync(policy + '.tmp', JSON.stringify({ contract: 1, revision: String(Date.now()),
    namespace: 'isolated', proxies, expires_at_ms: expires }));
  fs.renameSync(policy + '.tmp', policy);
}
publish(['127.0.0.2']);
let logs = '';
const child = spawn('node', [path.join(root, 'openclaw.mjs'), 'gateway', 'run', '--allow-unconfigured'], {
  env: { ...process.env, HOME: dir, OPENCLAW_STATE_DIR: dir, OPENCLAW_CONFIG_PATH: config,
    CLAWMANAGER_PROXY_POLICY_FILE: policy, OPENCLAW_SKIP_CHANNELS: '1' },
  stdio: ['ignore', 'pipe', 'pipe'],
});
child.stdout.on('data', b => { logs += b; });
child.stderr.on('data', b => { logs += b; });
function request(localAddress, auth = token, forwarded = '10.244.235.192') {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, localAddress, path: '/v1/chat/completions',
      method: 'POST', headers: { authorization: `Bearer ${auth}`, 'x-forwarded-for': forwarded,
        'content-type': 'application/json' } }, res => {
      let body = ''; res.on('data', b => { body += b; }); res.on('end', () => resolve({status: res.statusCode, body}));
    });
    req.setTimeout(3000, () => req.destroy(new Error('HTTP timeout')));
    req.on('error', reject); req.end('{}');
  });
}
function websocket(localAddress) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`ws://127.0.0.1:${port}`, { localAddress, handshakeTimeout: 3000,
      headers: { 'x-forwarded-for': '10.244.235.192', authorization: `Bearer ${token}` } });
    ws.on('open', () => { ws.close(); resolve(101); });
    ws.on('unexpected-response', (_, res) => { res.resume(); ws.terminate(); resolve(res.statusCode); });
    ws.on('error', reject);
  });
}
try {
  let ready = false;
  for (let i = 0; i < 180; i++) {
    if (child.exitCode !== null) throw new Error(`Gateway exited ${child.exitCode}: ${logs.slice(-6000)}`);
    try { await request('127.0.0.2'); ready = true; break; } catch {}
    await new Promise(r => setTimeout(r, 250));
  }
  assert.ok(ready, `Gateway not ready: ${logs.slice(-6000)}`);
  const before = fs.readFileSync(config, 'utf8');
  const pid = child.pid;
  assert.equal((await request('127.0.0.2')).status, 400, 'trusted proxy reaches validated API');
  assert.equal((await request('127.0.0.2', 'invalid-token')).status, 401, 'auth remains enforced');
  assert.equal((await request('127.0.0.3')).status, 403, 'untrusted sender cannot spoof forwarded client');
  assert.equal(await websocket('127.0.0.2'), 101, 'trusted WebSocket upgrades');
  assert.equal(await websocket('127.0.0.3'), 403, 'untrusted WebSocket rejected');
  publish(['127.0.0.3']);
  assert.equal((await request('127.0.0.3')).status, 400, 'new proxy accepted without config restart');
  assert.equal((await request('127.0.0.2')).status, 403, 'old proxy revoked');
  assert.equal(await websocket('127.0.0.3'), 101, 'WebSocket sees rotated trust');
  assert.equal(await websocket('127.0.0.2'), 403, 'WebSocket revokes old trust');
  publish(['127.0.0.3'], Date.now() - 1);
  assert.equal((await request('127.0.0.3')).status, 403, 'expired lease fails closed');
  publish(['10.244.0.0/16']);
  assert.equal((await request('127.0.0.3')).status, 403, 'broad policy fails closed');
  assert.equal(child.pid, pid); assert.equal(child.exitCode, null);
  assert.equal(fs.readFileSync(config, 'utf8'), before, 'policy must not rewrite user config');
  console.log('PASS: real OpenClaw HTTP/WebSocket rotation, spoof rejection, expiry and unchanged process/config');
} catch (error) {
  console.error(logs.slice(-6000)); throw error;
} finally {
  child.kill('SIGTERM');
  await Promise.race([new Promise(r => child.once('exit', r)), new Promise(r => setTimeout(r, 3000))]);
  if (child.exitCode === null) child.kill('SIGKILL');
  fs.rmSync(dir, { recursive: true, force: true });
}
