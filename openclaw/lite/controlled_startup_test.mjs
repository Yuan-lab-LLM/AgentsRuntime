import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';
import { loadOwnerAPI, ownerAPI, OwnerAdmissionTimeout, publicFailure, waitForOwner } from './controlled_startup.mjs';

const directory = '/usr/local/lib/node_modules/openclaw/dist';
const filename = fs.readdirSync(directory).find(name => /^gateway-state-owner-[A-Za-z0-9_-]+\.mjs$/.test(name) && fs.readFileSync(path.join(directory, name), 'utf8').includes('function resolveGatewayStateOwnerPath('));
assert.ok(filename, 'run acceptance with the real installed native OpenClaw package');
const installed = await import(pathToFileURL(path.join(directory, filename)).href);
const exported = name => Object.values(installed).find(value => typeof value === 'function' && value.name === name);
const native = await loadOwnerAPI();
assert.ok(native);

async function freshForeignOwner(run) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cm-native-owner-'));
  const db = path.join(root, 'home', '.openclaw', 'state', 'openclaw.sqlite');
  fs.mkdirSync(path.dirname(db), { recursive: true });
  fs.writeFileSync(db, '');
  const lock = exported('resolveGatewayStateOwnerPath')(db);
  fs.mkdirSync(path.dirname(lock), { recursive: true });
  const baseline = Date.now();
  const holder = {
    pid: 999999999, ownerId: 'disposable-owner', createdAt: new Date(baseline).toISOString(),
    stateDir: path.dirname(path.dirname(db)), configPath: path.join(path.dirname(path.dirname(db)), 'openclaw.json'), role: 'gateway',
    processNamespace: { ...exported('readGatewayLockProcessNamespace')(), pidNsInode: 'disposable-foreign-namespace' },
  };
  const bytes = JSON.stringify(holder);
  fs.writeFileSync(lock, bytes);
  fs.utimesSync(lock, new Date(baseline), new Date(baseline));
  const before = fs.statSync(lock);
  const clock = Date.now;
  let elapsed = 0;
  Date.now = () => baseline + elapsed;
  try {
    const options = { now: () => elapsed, delay: async ms => { elapsed += ms; } };
    assert.throws(() => native.check(db), native.NamespaceError);
    await run(() => native.check(db), options, () => elapsed);
    assert.equal(fs.readFileSync(lock, 'utf8'), bytes, 'the native owner file must remain untouched');
    const after = fs.statSync(lock);
    assert.equal(after.ino, before.ino);
    assert.equal(after.mtimeMs, before.mtimeMs);
    assert.equal(fs.readFileSync(db, 'utf8'), '', 'admission must not open or migrate SQLite');
  } finally {
    Date.now = clock;
    fs.rmSync(root, { recursive: true, force: true });
  }
}

test('actual native foreign owner stays protected until its 90 second heartbeat expires', async () => {
  await freshForeignOwner(async (check, options, elapsed) => {
    let pending = 0;
    await waitForOwner(check, native.NamespaceError, { ...options, timeoutMs: 120000, onPending: () => pending++ });
    assert.ok(elapsed() > 90000 && elapsed() < 120000);
    assert.equal(pending, 1);
  });
});

test('a shorter configured startup budget fails boundedly without clearing the native owner', async () => {
  await freshForeignOwner(async (check, options, elapsed) => {
    await assert.rejects(waitForOwner(check, native.NamespaceError, { ...options, timeoutMs: 30000 }), OwnerAdmissionTimeout);
    assert.equal(elapsed(), 30000);
    assert.throws(check, native.NamespaceError);
  });
});

test('unrelated native errors and name-only lookalikes are never retried or leaked', async () => {
  for (const name of ['Error', 'GatewayLockNamespaceError', 'credential-secret-in-name']) {
    const error = new Error('provider-key-secret redis://credential-secret');
    error.name = name;
    let attempts = 0;
    await assert.rejects(waitForOwner(() => { attempts++; throw error; }, native.NamespaceError, {
      timeoutMs: 120000, delay: () => assert.fail('unrelated error was delayed'),
    }), caught => caught === error);
    assert.equal(attempts, 1);
    assert.ok(!publicFailure(error).includes('secret'));
    assert.ok(!publicFailure(error).includes('redis'));
  }
});

test('native API discovery rejects ambiguous or missing exports', () => {
  const make = name => Object.defineProperty(() => {}, 'name', { value: name });
  const check = make('assertStateDatabaseAccessAllowed');
  const NamespaceError = make('GatewayLockNamespaceError');
  assert.throws(() => ownerAPI({ check }), /native_owner_api_unavailable/);
  assert.throws(() => ownerAPI({ check, duplicate: make(check.name), NamespaceError }), /native_owner_api_ambiguous/);
  assert.equal(ownerAPI({ check, alias: check, NamespaceError }).check, check);
});
