// Resolve only Gateway-owned routes. No session-file writes, fallback models,
// persistent grant cache, or hidden aliases in the visible model catalog.
import { AsyncLocalStorage } from 'node:async_hooks';
import { readFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';
const turnContext = new AsyncLocalStorage();
const taskProtocol = () => process.env.CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL === '1' &&
  !/^(1|true|yes|on|enabled)$/i.test((process.env.CLAWMANAGER_TEAM_ENABLED ?? '').trim());

function taskError() {
  return Object.assign(new Error('当前模型任务不可用：权限已撤销或任务状态暂不可用'), { code: 'MODEL_UNAVAILABLE' });
}

export function verifiedHarnessGroup(stat, rawGroup, pid = process.pid) {
  if (!/^\d{1,10}$/.test(rawGroup ?? '') || Number(rawGroup) <= 1 || !Number.isSafeInteger(pid) || pid <= 1) return;
  const end = stat.lastIndexOf(') ');
  if (end < 0 || Number(stat.slice(0, stat.indexOf(' '))) !== pid) return;
  const fields = stat.slice(end + 2).trim().split(/\s+/);
  const group = Number(rawGroup);
  if (Number(fields[2]) === group && Number(fields[3]) === group) return group;
}

export function terminateHarnessRuntime({ read = readFileSync, kill = process.kill, exit = process.exit,
  platform = process.platform, pid = process.pid } = {}) {
  // Only the Pro launcher publishes a private session; Lite delegates process
  // group cleanup to its supervisor after this process's explicit exit.
  if (platform === 'linux') {
    let group;
    try { group = verifiedHarnessGroup(read('/proc/self/stat', 'utf8'), process.env.CLAWMANAGER_HARNESS_FAIL_STOP_PGID, pid); } catch {}
    if (group) {
      try { kill(-group, 'SIGKILL'); }
      catch { console.error('ClawManager private group stop failed; cleanup remains unconfirmed'); }
    }
  }
  exit(78);
}

export function createHarnessFailStop({ terminate = terminateHarnessRuntime, schedule = setTimeout } = {}) {
  let pending = false;
  return {
    assertAdmission() { if (pending) throw taskError(); },
    arm() {
      if (pending || !taskProtocol() || process.env.CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE !== '1') return;
      const raw = process.env.CLAWMANAGER_HARNESS_CANCEL_GRACE_SECONDS ?? '';
      const seconds = /^\d{1,2}$/.test(raw) ? Math.max(1, Math.min(60, Number(raw))) : 10;
      pending = true;
      // Deliberately referenced: shutdown must not disappear when the native
      // turn returns. Regrant cannot prove cleanup or cancel this fallback.
      schedule(terminate, seconds * 1000);
    },
  };
}
const failStop = createHarnessFailStop();

async function taskRequest(task, action, signal) {
  const url = new URL(task.base);
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw taskError();
  let response;
  try {
    response = await fetch(task.base + '/runtime/tasks/' + action, {
      method: 'POST', redirect: 'error',
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(2500)]) : AbortSignal.timeout(2500),
      headers: { Authorization: 'Bearer ' + task.key, 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: task.session, task_id: task.id, generation: task.generation,
        ...(action === 'finish' ? { cancelled: false } : {}) }),
    });
    if (response.status !== 200) {
      const error = taskError();
      error.revoked = action === 'check' && response.status === 403 && response.headers.get('X-ClawManager-Task-State') === 'revoked';
      throw error;
    }
  } catch (error) { if (error?.revoked === true) throw error; throw taskError(); }
  finally { await response?.body?.cancel().catch(() => {}); }
}

export async function withManagedTurn(machine, operation) {
  if (!taskProtocol()) return operation();
  const profile = await ownedProfile(machine.loopCtx.llm, machine.options.provider);
  if (!profile) return operation(); // Independent providers retain native behavior.
  failStop.assertAdmission();
  const phase = machine.phase;
  const turnAbort = phase.abort;
  const session = machine.session.id;
  const turn = phase.turn + 1;
  const rawGeneration = process.env.CLAWMANAGER_GATEWAY_GENERATION ?? '';
  if (phase.kind !== 'running' || typeof session !== 'string' || !session.trim() || session.length > 256 ||
      /[\x00-\x1f\x7f]/.test(session) || !Number.isSafeInteger(turn) || turn <= 0 ||
      !/^\d{1,10}$/.test(rawGeneration) || Number(rawGeneration) > 2147483647) throw taskError();
  const task = { base: baseOf(profile.baseURL), key: profile.apiKey, session,
    id: 'turn-' + turn, generation: Number(rawGeneration), closed: false, failure: undefined };
  await taskRequest(task, 'check');
  const stop = new AbortController();
  const watcher = (async () => {
    try {
      while (true) {
        await delay(1000, undefined, { signal: stop.signal });
        await taskRequest(task, 'check', stop.signal);
      }
    } catch (error) {
      if (!stop.signal.aborted) {
        task.failure = taskError();
        if (error?.revoked === true) failStop.arm();
        if (machine.phase === phase && phase.abort === turnAbort) {
          try { machine.cancel({ kind: 'hook', reason: 'model access unavailable' }); } catch {}
        }
      }
    }
  })();
  try {
    const result = await turnContext.run(task, operation);
    if (task.failure) throw task.failure;
    stop.abort();
    await watcher;
    if (task.failure) throw task.failure;
    try { await taskRequest(task, 'check'); }
    catch (error) { if (error?.revoked === true) failStop.arm(); throw error; }
    await taskRequest(task, 'finish'); // Ordinary completion, NEVER a cleanup receipt.
    return result;
  } finally {
    task.closed = true;
    stop.abort();
    await watcher; // Never leave a poll capable of cancelling the next turn.
  }
}

export function managedRequestHeaders(baseURL, apiKey, original) {
  if (!taskProtocol() || baseOf(baseURL) !== baseOf(process.env.CLAWMANAGER_LLM_BASE_URL) ||
      !apiKey || apiKey !== process.env.CLAWMANAGER_LLM_API_KEY?.trim()) return original;
  failStop.assertAdmission();
  const task = turnContext.getStore();
  if (!task || task.closed || task.failure || task.base !== baseOf(baseURL) || task.key !== apiKey) throw taskError();
  const headers = new Headers(original);
  headers.set('X-ClawManager-Session-ID', task.session);
  headers.set('X-ClawManager-Task-ID', task.id);
  headers.set('X-ClawManager-Runtime-Generation', String(task.generation));
  return Object.fromEntries(headers);
}
const limit = 1024 * 1024;
const norm = value => String(value ?? '').trim().toLowerCase();
const baseOf = value => String(value ?? '').trim().replace(/\/+$/, '');
const namesOf = item => new Set([...(item.legacy_name_unavailable ? [] : [item.id]), item.stable_id, ...(item.aliases ?? [])].filter(Boolean).map(norm));
export const enabled = () => process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL === '1';

export async function readCatalog(signal, ErrorType = Error) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 2500);
  const combined = signal ? AbortSignal.any([signal, controller.signal]) : controller.signal;
  let reader;
  try {
    const base = baseOf(process.env.CLAWMANAGER_LLM_BASE_URL);
    const key = process.env.CLAWMANAGER_LLM_API_KEY?.trim();
    const url = new URL(base);
    if (!key || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error('invalid endpoint');
    const response = await fetch(base + '/runtime/catalog', {
      headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' },
      redirect: 'error', signal: combined,
    });
    if (!response.ok || !response.body) throw new Error('catalog refused');
    reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) throw new Error('oversized catalog');
      chunks.push(Buffer.from(value));
    }
    const payload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!Array.isArray(payload.models)) throw new Error('invalid catalog');
    for (const item of payload.models) {
      if (!item || typeof item.id !== 'string' || !item.id.trim() ||
          item.id !== 'auto' && (typeof item.stable_id !== 'string' || !item.stable_id.trim()) ||
          item.legacy_name_unavailable !== undefined && typeof item.legacy_name_unavailable !== 'boolean' ||
          !Array.isArray(item.aliases ?? []) || !(item.aliases ?? []).every(v => typeof v === 'string') ||
          !Array.isArray(item.provider_refs ?? []) || !(item.provider_refs ?? []).every(ref =>
            ref && typeof ref.provider_id === 'string' && ref.provider_id.trim() && typeof ref.model_id === 'string' && ref.model_id.trim())) {
        throw new Error('invalid identity metadata');
      }
    }
    return payload.models;
  } catch {
    if (signal?.aborted) throw new ErrorType('模型调用已取消', 'ABORTED');
    throw new ErrorType('模型授权目录暂不可用，请稍后重试；未切换其他模型', 'MODEL_CATALOG_UNAVAILABLE');
  } finally {
    clearTimeout(timer);
    await reader?.cancel().catch(() => {});
  }
}

async function ownedProfile(registry, provider) {
  const adapter = registry.adapters.get(provider)?.adapter;
  if (typeof adapter?.clawmanagerModelProfile !== 'function') return;
  const profile = await adapter.clawmanagerModelProfile(provider);
  if (!profile || baseOf(profile.baseURL) !== baseOf(process.env.CLAWMANAGER_LLM_BASE_URL) ||
      !profile.apiKey || profile.apiKey !== process.env.CLAWMANAGER_LLM_API_KEY?.trim()) return;
  return profile;
}

function exactPair(item, provider, model) {
  return (item.provider_refs ?? []).some(ref => norm(ref.provider_id) === norm(provider) && norm(ref.model_id) === norm(model));
}

export async function resolveManagedSelection(registry, selection, signal, ErrorType) {
  if (!enabled()) return selection;
  signal?.throwIfAborted();
  const registered = registry.adapters.has(selection.provider);
  const owned = await ownedProfile(registry, selection.provider);
  // Existing custom/built-in routes always win, even when their name happens
  // to match a retired managed provider. Never replace their credentials.
  if (registered && !owned) return selection;
  const items = await readCatalog(signal, ErrorType);
  const hits = items.filter(item => exactPair(item, selection.provider, selection.model) ||
    (owned || norm(selection.provider) === 'clawmanager') && namesOf(item).has(norm(selection.model)));
  if (hits.length !== 1) throw new ErrorType('当前模型不可用：模型已撤权、删除或历史引用不唯一', 'MODEL_UNAVAILABLE');
  const item = hits[0];
  const names = namesOf(item);
  const candidates = [];
  for (const provider of registry.adapters.keys()) {
    const profile = provider === selection.provider ? owned : await ownedProfile(registry, provider);
    if (!profile) continue;
    for (const model of profile.models) {
      if (!names.has(norm(model.id))) continue;
      // A native route ultimately sends a bare model ID. Prove it refers to
      // this same deployment, not merely a coincidentally matching leaf name.
      const wireHits = items.filter(entry => namesOf(entry).has(norm(model.id)));
      if (wireHits.length !== 1 || wireHits[0] !== item) continue;
      candidates.push({ provider, model: model.id });
    }
  }
  candidates.sort((a, b) => Number(b.model === item.id) - Number(a.model === item.id) ||
    Number(b.provider === selection.provider) - Number(a.provider === selection.provider) ||
    a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model));
  const target = candidates[0];
  if (!target) throw new ErrorType('当前模型不可用：实例尚未加载该模型配置，请刷新配置或重启实例', 'MODEL_UNAVAILABLE');
  signal?.throwIfAborted();
  if (target.provider === selection.provider && target.model === selection.model) return selection;
  return { ...selection, ...target };
}

export async function filterManagedModels(registry, provider, models, ErrorType) {
  if (!enabled() || !await ownedProfile(registry, provider)) return models;
  const items = await readCatalog(undefined, ErrorType);
  // Historical names remain lookup-only. Do not clutter the UI with aliases.
  return models.filter(model => items.some(item => (!item.legacy_name_unavailable && norm(item.id) === norm(model.id)) || norm(item.stable_id) === norm(model.id)));
}
