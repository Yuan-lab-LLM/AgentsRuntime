// Run against the installed core, on either an ordinary filesystem or a real NFS mount.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';

const core = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const dep = path.join(core, 'node_modules/@openclaw/fs-safe/dist');
const { root } = await import(pathToFileURL(path.join(dep, 'index.js')));
const { __setFsSafeTestHooksForTest: hooks } = await import(pathToFileURL(path.join(dep, 'test-hooks.js')));
const { configureFsSafeNative } = await import(pathToFileURL(path.join(dep, 'native-config.js')));
process.env.NODE_ENV = 'test';
const base = process.env.WORKSPACE_PUBLICATION_TEST_ROOT || '/tmp';
const dir = fs.mkdtempSync(path.join(base, 'cm-publication-'));
// Do not let native workspace state/leases use the image's ambient /config.
process.env.HOME = dir;
process.env.OPENCLAW_STATE_DIR = path.join(dir, 'state');
process.env.OPENCLAW_CONFIG_PATH = path.join(dir, 'state/openclaw.json');
fs.mkdirSync(process.env.OPENCLAW_STATE_DIR);
const options = { atomic: true, durable: 'file', mkdir: false, mode: 0o600 };
let count = 0;
function hasError(error, match) {
  if (!error) return false;
  return match(error) || hasError(error.cause, match) ||
    (Array.isArray(error.errors) && error.errors.some(e => hasError(e, match)));
}
async function rejection(run, code) {
  await assert.rejects(run, e => hasError(e, x => x.code === code));
  count++;
}
try {
  const mounts = fs.readFileSync('/proc/mounts', 'utf8').split('\n');
  console.log('FILESYSTEM', mounts.filter(x => / nfs4? /.test(x)).join('\n') || 'local control');
  if (process.env.EXPECT_UNPATCHED_NFS_FAILURE === '1') {
    const target = path.join(dir, 'AGENTS.md');
    let observed;
    hooks({ afterPinnedWriteFallbackRename(p) {
      observed = { nlink: Number(fs.statSync(p).nlink), names: fs.readdirSync(dir) };
    } });
    await assert.rejects(() => root(dir).then(r => r.create(target, 'fixture', options)), e =>
      e.message === 'staged file publish failed' && e.details?.publication?.status === 'published' &&
      hasError(e, x => x.code === 'hardlink'));
    assert.equal(fs.readFileSync(target, 'utf8'), 'fixture');
    assert.equal(fs.statSync(target).nlink, 1);
    assert.ok(observed.nlink > 1 && observed.names.some(n => n.startsWith('.nfs')));
    console.log('CONFIRMED unpatched atomic publication fails while own NFS temporary alias remains', observed);
  } else {
    for (let index = 0; index < 12; index++) {
      const target = path.join(dir, `fresh-${index}.md`);
      await (await root(dir)).create(target, `fixture-${index}`, options);
      assert.equal(fs.readFileSync(target, 'utf8'), `fixture-${index}`);
      assert.equal(fs.statSync(target).nlink, 1);
      assert.equal(fs.statSync(target).mode & 0o777, 0o600);
      count++;
    }
    const first = path.join(dir, 'fresh-0.md');
    await rejection(() => root(dir).then(r => r.create(first, 'overwrite forbidden', options)), 'already-exists');
    assert.equal(fs.readFileSync(first, 'utf8'), 'fixture-0');
    for (const mode of [0o000, 0o200, 0o644]) {
      const target = path.join(dir, `mode-${mode}.md`);
      await (await root(dir)).create(target, 'mode fixture', { ...options, mode });
      assert.equal(fs.statSync(target).mode & 0o777, mode);
      fs.chmodSync(target, 0o600); // Only the fixture owner's test file.
      assert.equal(fs.readFileSync(target, 'utf8'), 'mode fixture');
      count++;
    }
    const same = path.join(dir, 'concurrent.md');
    const results = await Promise.allSettled([1, 2].map(n => root(dir).then(r => r.create(same, `winner-${n}`, options))));
    assert.equal(results.filter(x => x.status === 'fulfilled').length, 1);
    assert.ok(results.some(x => x.status === 'rejected' && hasError(x.reason, e => e.code === 'already-exists')));
    assert.match(fs.readFileSync(same, 'utf8'), /^winner-[12]$/);
    count++;

    // Exercise the same fallback verifier on a local filesystem too; production stays auto.
    configureFsSafeNative({ mode: 'off' });
    const outside = path.join(dir, 'untouched.txt');
    fs.writeFileSync(outside, 'must stay unchanged');
    const linked = path.join(dir, 'late-hardlink.md');
    hooks({ afterPinnedWriteFallbackRename(p) { if (p === linked) fs.linkSync(p, p + '.extra'); } });
    await rejection(() => root(dir).then(r => r.create(linked, 'owned fixture', options)), 'hardlink');
    const swapped = path.join(dir, 'late-symlink.md');
    hooks({ afterPinnedWriteFallbackRename(p) { if (p === swapped) { fs.unlinkSync(p); fs.symlinkSync(outside, p); } } });
    await assert.rejects(() => root(dir).then(r => r.create(swapped, 'never outside', options)), e =>
      hasError(e, x => ['symlink', 'path-mismatch'].includes(x.code)));
    assert.equal(fs.readFileSync(outside, 'utf8'), 'must stay unchanged');
    count++;
    const replaced = path.join(dir, 'late-replacement.md');
    hooks({ afterPinnedWriteFallbackRename(p) { if (p === replaced) { fs.unlinkSync(p); fs.writeFileSync(p, 'same content'); } } });
    await rejection(() => root(dir).then(r => r.create(replaced, 'same content', options)), 'path-mismatch');
    hooks(undefined);
    const parent = path.join(dir, 'parent');
    fs.mkdirSync(parent);
    const parentRoot = await root(parent);
    hooks({ afterPinnedWriteFallbackRename(p) {
      if (p === path.join(parent, 'guarded.md')) { fs.renameSync(parent, parent + '-original'); fs.mkdirSync(parent); }
    } });
    await assert.rejects(() => parentRoot.create(path.join(parent, 'guarded.md'), 'guarded', options), e =>
      hasError(e, x => ['path-mismatch', 'ENOENT'].includes(x.code)));
    count++;
    assert.equal(fs.existsSync(path.join(parent, 'guarded.md')), false);
    hooks(undefined);

    // A close failure after publication is still a failure, not a silently accepted create.
    const open = fsp.open;
    let injected = false;
    fsp.open = async (...args) => {
      const handle = await open(...args);
      if (String(args[0]).includes('.fs-safe-')) {
        const close = handle.close.bind(handle);
        handle.close = async () => { await close(); if (!injected) { injected = true; throw new Error('synthetic close failure'); } };
      }
      return handle;
    };
    try {
      await assert.rejects(() => root(dir).then(r => r.create(path.join(dir, 'close-failure.md'), 'fixture', options)), e =>
        hasError(e, x => x.message === 'synthetic close failure'));
      assert.equal(injected, true);
      count++;
    } finally { fsp.open = open; }
    configureFsSafeNative({ mode: 'auto' });
    const workspaceModule = fs.readdirSync(path.join(core, 'dist')).find(n => n.startsWith('workspace-') &&
      n.endsWith('.mjs') && fs.readFileSync(path.join(core, 'dist', n), 'utf8').includes('async function ensureAgentWorkspace('));
    const workspace = path.join(dir, 'actual-workspace');
    // SQLite worker descriptors belong to this child; join it before deleting its NFS state.
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', `
      import fs from 'node:fs'; import assert from 'node:assert/strict';
      const { t: ensureAgentWorkspace } = await import(${JSON.stringify(pathToFileURL(path.join(core, 'dist', workspaceModule)).href)});
      const dir = ${JSON.stringify(workspace)};
      await ensureAgentWorkspace({ dir, ensureBootstrapFiles: true });
      for (const name of ['AGENTS.md', 'SOUL.md', 'IDENTITY.md', 'USER.md']) assert.ok(fs.statSync(dir+'/'+name).size > 0);
      fs.writeFileSync(dir+'/SOUL.md', 'user-owned memory fixture');
      await ensureAgentWorkspace({ dir, ensureBootstrapFiles: true });
      assert.equal(fs.readFileSync(dir+'/SOUL.md','utf8'), 'user-owned memory fixture');
      console.log('PASS actual native workspace cold initialization and existing memory');
    `], { env: process.env, encoding: 'utf8', timeout: 60000 });
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    assert.match(result.stdout, /PASS actual native workspace/);
    count++;
    console.log(`PASS ${count} actual installed-core publication/security/bootstrap checks`);
  }
} finally {
  hooks(undefined);
  configureFsSafeNative({ mode: 'auto' });
  assert.ok(path.dirname(dir) === path.resolve(base) && path.basename(dir).startsWith('cm-publication-'));
  fs.rmSync(dir, { recursive: true, force: true });
}
