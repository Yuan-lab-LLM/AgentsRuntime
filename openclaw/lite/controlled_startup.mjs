import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const nativeDirectory = '/usr/local/lib/node_modules/openclaw/dist';

export function ownerAPI(exports) {
  const find = name => {
    const matches = [...new Set(Object.values(exports))].filter(value => typeof value === 'function' && value.name === name);
    if (matches.length === 0) throw new Error('native_owner_api_unavailable');
    if (matches.length !== 1) throw new Error('native_owner_api_ambiguous');
    return matches[0];
  };
  return {
    check: find('assertStateDatabaseAccessAllowed'),
    NamespaceError: find('GatewayLockNamespaceError'),
  };
}

export async function loadOwnerAPI() {
  if (!fs.existsSync(nativeDirectory)) return null;
  const names = fs.readdirSync(nativeDirectory).filter(name => /^gateway-state-owner-[A-Za-z0-9_-]+\.mjs$/.test(name));
  // Versions predating native state ownership retain their original CLI path.
  if (names.length === 0) return null;
  const candidates = [];
  for (const name of names) {
    const file = path.join(nativeDirectory, name);
    if (!fs.lstatSync(file).isFile() || fs.realpathSync(file) !== file) throw new Error('native_owner_module_untrusted');
    const exports = await import(pathToFileURL(file).href);
    let api;
    try { api = ownerAPI(exports); }
    catch (error) {
      // Native packages also ship a public facade without the namespace error.
      if (error.message === 'native_owner_api_unavailable') continue;
      throw error;
    }
    if (!candidates.some(existing => existing.check === api.check && existing.NamespaceError === api.NamespaceError)) candidates.push(api);
  }
  if (candidates.length !== 1) throw new Error('native_owner_api_ambiguous');
  return candidates[0];
}

export class OwnerAdmissionTimeout extends Error {}

export async function waitForOwner(check, NamespaceError, {
  timeoutMs, now = () => performance.now(), delay = ms => new Promise(resolve => setTimeout(resolve, ms)), onPending = () => {},
}) {
  const deadline = now() + timeoutMs;
  let pending = false;
  for (;;) {
    if (pending && now() >= deadline) throw new OwnerAdmissionTimeout('native_owner_admission_timeout');
    try { check(); return; }
    catch (error) {
      if (!(error instanceof NamespaceError)) throw error;
      if (!pending) { pending = true; onPending(); }
      const remaining = deadline - now();
      if (remaining <= 0) throw new OwnerAdmissionTimeout('native_owner_admission_timeout');
      await delay(Math.min(1000, remaining));
    }
  }
}

export function publicFailure(error) {
  if (error instanceof OwnerAdmissionTimeout) return 'native_owner_admission_timeout';
  const known = new Set(['GatewayLockNamespaceError', 'GatewayStateOwnerContentionError', 'Error', 'TypeError', 'RangeError']);
  return `native_owner_admission_rejected kind=${known.has(error?.name) ? error.name : 'NativeError'}`;
}

if (process.argv[1] === '--cm-openclaw-owner-admission') {
  try {
    const timeoutMs = Number(process.argv[2]);
    if (!Number.isInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > 120000) throw new Error('invalid_budget');
    const native = await loadOwnerAPI();
    if (native) {
      const state = process.env.OPENCLAW_STATE_DIR || path.join(process.env.HOME, '.openclaw');
      await waitForOwner(() => native.check(path.join(state, 'state', 'openclaw.sqlite')), native.NamespaceError, {
        timeoutMs,
        onPending: () => console.error('native_owner_admission_pending kind=GatewayLockNamespaceError'),
      });
    }
    console.error(native ? 'native_owner_admission_ready' : 'native_owner_admission_legacy');
  } catch (error) {
    // Native messages may include workspace configuration; keep diagnostics
    // useful without emitting arbitrary messages, stack traces or credentials.
    console.error(publicFailure(error));
    process.exitCode = 1;
  }
}
