// Exercise the installed dependency used by Doctor, not a reimplementation.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
const core = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const dep = path.join(core, 'node_modules/@openclaw/fs-safe/dist');
const { publishFileExclusive } = await import(pathToFileURL(path.join(dep, 'publish-file.js')));
const { __setFsSafeTestHooksForTest: hooks } = await import(pathToFileURL(path.join(dep, 'test-hooks.js')));
process.env.NODE_ENV = 'test';
const dir = fs.mkdtempSync(path.join(process.env.UPGRADE_PUBLICATION_TEST_ROOT || '/tmp', 'cm-upgrade-move-'));
let count = 0;
try {
  const move = (sourcePath, targetPath, expectedSourceIdentity = fs.lstatSync(sourcePath, { bigint: true })) =>
    publishFileExclusive({ sourcePath, targetPath, expectedSourceIdentity, strategy: 'rename-noreplace', onSyncFailure: 'preserve' });
  const source = path.join(dir, 'original'), target = path.join(dir, 'published');
  fs.writeFileSync(source, 'durable original', { mode: 0o600 });
  if (process.env.EXPECT_UNPATCHED_MOVE_FAILURE === '1') {
    await assert.rejects(() => move(source, target), /exclusive file publication failed/);
    assert.equal(fs.readFileSync(target, 'utf8'), 'durable original');
    console.log('CONFIRMED: actual Doctor no-replace publication fails on NFS after publishing bytes');
  } else {
    const before = fs.readdirSync('/proc/self/fd').length;
    for (let i = 0; i < 100; i++) {
      const a = path.join(dir, `source-${i}`), b = path.join(dir, `target-${i}`);
      fs.writeFileSync(a, `snapshot-${i}`, { mode: 0o600 });
      const identity = fs.lstatSync(a, { bigint: true });
      const result = await move(a, b, identity);
      assert.equal(result.method, 'rename-noreplace');
      assert.equal(fs.existsSync(a), false);
      const after = fs.lstatSync(b, { bigint: true });
      assert.equal(after.ino, identity.ino);
      assert.equal(after.nlink, 1n);
      assert.equal(Number(after.mode) & 0o777, 0o600);
      assert.equal(fs.readFileSync(b, 'utf8'), `snapshot-${i}`);
      count++;
    }
    assert.equal(fs.readdirSync('/proc/self/fd').length, before, 'publication leaked file descriptors');
    fs.writeFileSync(target, 'existing');
    await assert.rejects(() => move(source, target));
    assert.equal(fs.readFileSync(target, 'utf8'), 'existing');
    assert.equal(fs.readFileSync(source, 'utf8'), 'durable original');
    const wrong = fs.statSync(target, { bigint: true });
    await assert.rejects(() => move(source, path.join(dir, 'wrong'), wrong));
    assert.equal(fs.existsSync(path.join(dir, 'wrong')), false);
    fs.symlinkSync(source, path.join(dir, 'symlink'));
    await assert.rejects(() => move(path.join(dir, 'symlink'), path.join(dir, 'escaped')));
    const nfs=fs.statfsSync(dir).type===0x6969;
    if(nfs) {
      fs.linkSync(source, path.join(dir, 'hardlink'));
      await assert.rejects(() => move(source, path.join(dir, 'multiply-linked')));
      assert.equal(fs.existsSync(source), true);
    }
    for (const attack of ['replace', 'symlink', ...(nfs?['extra-link']:[])]) {
      const a = path.join(dir, `attack-${attack}`), b = a + '-target';
      fs.writeFileSync(a, 'original');
      hooks({ afterPublishTargetCreated(_method, published) {
        if (attack === 'extra-link') fs.linkSync(published, published + '-extra');
        else {
          fs.renameSync(published, published + '-retained');
          if (attack === 'replace') fs.writeFileSync(published, 'replacement');
          else fs.symlinkSync(target, published);
        }
      } });
      try { await assert.rejects(() => move(a, b)); } finally { hooks(undefined); }
      assert.equal(fs.existsSync(b), true, 'ambiguous publication must retain evidence');
    }
    for (const attack of ['replace-parent','symlink-parent']) {
      const a=path.join(dir,attack+'-source'), parent=path.join(dir,attack), retained=parent+'-retained';
      fs.mkdirSync(parent); fs.writeFileSync(a,'protected original');
      hooks({afterPublishTargetCreated() {
        fs.renameSync(parent,retained);
        if(attack==='replace-parent')fs.mkdirSync(parent);
        else fs.symlinkSync(retained,parent);
      }});
      try {await assert.rejects(()=>move(a,path.join(parent,'target')));}finally{hooks(undefined);}
      assert.equal(fs.readFileSync(path.join(retained,'target'),'utf8'),'protected original');
    }
    console.log(`PASS: ${count} durable no-replace moves, FD stability, no-clobber, identity, symlink and hardlink guards`);
  }
} finally {
  hooks(undefined);
  // Only the fresh fixture directory created above, never a workspace or export root.
  fs.rmSync(dir, { recursive: true, force: true });
}
