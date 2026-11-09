// Disposable local fixture matching legacy image-owned npm symlink topology.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
const base = process.env.DOCTOR_FIXTURE_ROOT || '/tmp';
const dir = fs.mkdtempSync(path.join(base, 'cm-doctor-shared-'));
const home = path.join(dir, 'home'), state = path.join(home, '.openclaw');
fs.mkdirSync(state, { recursive: true });
const defaults = '/defaults/.openclaw';
for (const name of ['state', 'config-journal-fingerprint.key']) {
  if (fs.existsSync(path.join(defaults, name))) fs.cpSync(path.join(defaults, name), path.join(state, name), { recursive: true });
}
// Clone only immutable image fixtures; never use production snapshots.
const config = JSON.parse(fs.readFileSync(path.join(defaults, 'openclaw.json')));
delete config.models; delete config.channels; delete config.auth;
config.gateway = { mode: 'local', auth: { mode: 'token', token: 'local-doctor-fixture' } };
config.agents = { defaults: { workspace: path.join(state, 'workspace') } };
config.update = { checkOnStart: false, auto: { enabled: false } };
const enabled = process.env.DOCTOR_ACTIVE_PLUGINS === '1';
config.plugins = { load: { paths: ['/usr/local/lib/node_modules/openclaw/dist/extensions/redis-team'] }, entries: { 'redis-team': { enabled: false }, feishu: { enabled }, 'wecom-openclaw-plugin': { enabled } } };
fs.writeFileSync(path.join(state, 'openclaw.json'), JSON.stringify(config));
function links(source, target, depth = 0) {
  fs.mkdirSync(target, { recursive: true });
  for (const e of fs.readdirSync(source, { withFileTypes: true })) {
    const a = path.join(source, e.name), b = path.join(target, e.name);
    if (depth >= 3 && !e.name.startsWith('@')) fs.symlinkSync(a, b);
    else if (e.isDirectory()) links(a, b, depth + 1);
    else if (e.isSymbolicLink()) fs.symlinkSync(fs.realpathSync(a), b);
    else fs.copyFileSync(a, b);
  }
}
links(path.join(defaults, 'npm'), path.join(state, 'npm'));
function own(p) { fs.lchownSync(p, 200518, 200518); const s = fs.lstatSync(p); if (s.isDirectory()) { fs.chmodSync(p, 0o700); for (const n of fs.readdirSync(p)) own(path.join(p, n)); } }
own(dir);
Object.assign(process.env, { HOME: home, OPENCLAW_STATE_DIR: state, OPENCLAW_CONFIG_PATH: path.join(state, 'openclaw.json') });
process.setgroups([]); process.setgid(200518); process.setuid(200518);
const records = {
    feishu: { source: 'npm', spec: '@openclaw/feishu', version: '2026.8.1', installPath: path.join(state, 'npm/projects/openclaw-feishu-dc69f44688/node_modules/@openclaw/feishu') },
    'wecom-openclaw-plugin': { source: 'npm', spec: '@wecom/wecom-openclaw-plugin', version: '2026.8.17', installPath: path.join(state, 'npm/projects/wecom-wecom-openclaw-plugin-18f843d908/node_modules/@wecom/wecom-openclaw-plugin') },
};
const { a: writeRecords } = await import('/usr/local/lib/node_modules/openclaw/dist/installed-plugin-index-records-CzJqH-fd.mjs');
const { i: withLease } = await import('/usr/local/lib/node_modules/openclaw/dist/plugin-lifecycle-lease-5X2Of1QR.mjs');
await withLease({env:process.env}, lease => writeRecords(records,{env:process.env,config,lease}));
if (process.env.DOCTOR_POST_CORE_ONLY === '1') {
  const { t: converge } = await import('/usr/local/lib/node_modules/openclaw/dist/post-core-plugin-convergence-BtMcGJP9.mjs');
  const result = await converge({ cfg: config, env: process.env, baselineInstallRecords: records, configPersistence: 'caller', timeoutMs: 3000, workTimeoutMs: 3000 });
  console.log(JSON.stringify({ enabled, errored: result.errored, smokeFailures: result.smokeFailures, warnings: result.warnings, checkedPlugins: Object.keys(result.installRecords ?? {}) }, null, 2));
  assert.equal(result.errored, false);
  process.exit(0);
}
const command = spawn('openclaw', ['doctor', '--fix', '--yes', '--non-interactive'], {
  uid: 200518, gid: 200518, cwd: dir, detached: true, stdio:['ignore','pipe','pipe'],
  env: { ...process.env, HOME: home, OPENCLAW_STATE_DIR: state, OPENCLAW_CONFIG_PATH: path.join(state, 'openclaw.json'), TMPDIR: dir, NO_COLOR: '1', OPENCLAW_NO_RESPAWN: '1', CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION: '1' },
});
// Only local synthetic fixture diagnostics; preserve the fixture when it fails.
for (const stream of [command.stdout,command.stderr]) stream.on('data', chunk=>process.stdout.write(chunk));
const timer=setTimeout(()=>{process.kill(-command.pid,'SIGKILL');},180000);
const code=await new Promise((resolve,reject)=>{command.once('error',reject);command.once('close',resolve);});
clearTimeout(timer);
try {process.kill(-command.pid,'SIGKILL');} catch(e) {if(e.code!=='ESRCH')throw e;}
assert.equal(code, 0, `Doctor failed in retained local fixture ${dir}`);
console.log('PASS: actual Doctor with image-owned shared plugin links and production-shaped UID/GID');
