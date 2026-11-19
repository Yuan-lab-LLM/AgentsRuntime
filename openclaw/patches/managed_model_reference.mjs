// OpenClaw 2026.10.2 managed-reference adapter. Never writes session history,
// rewrites arbitrary providers, caches grants, or substitutes another deployment.
import {RuntimeTaskError} from './runtime_task_guard.mjs';
const norm = value => String(value ?? '').trim().toLowerCase();
const baseOf = value => String(value ?? '').trim().replace(/\/+$/, '');
const namesOf = item => new Set([item.id, item.stable_id, ...(item.aliases ?? [])].filter(Boolean).map(norm));
export const enabled = () => process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL === '1';
export class ManagedModelError extends Error {
  constructor(message, code = 'MODEL_UNAVAILABLE') {
    super(message);
    this.name = 'ClawManagerModelReferenceError';
    this.code = code;
  }
}
export const isManagedModelError = error => error instanceof ManagedModelError || error instanceof RuntimeTaskError;
const unavailable = () => new ManagedModelError('当前模型不可用：模型已撤权、删除、历史引用不唯一或实例配置尚未更新；未切换其他模型');

export async function readCatalog(signal) {
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), 2500);
  const combined = signal ? AbortSignal.any([signal, timeout.signal]) : timeout.signal;
  let reader;
  try {
    const base = baseOf(process.env.CLAWMANAGER_LLM_BASE_URL);
    const key = process.env.CLAWMANAGER_LLM_API_KEY?.trim();
    const url = new URL(base);
    if (!key || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error();
    const response = await fetch(base + '/runtime/catalog', {
      headers: { Authorization: 'Bearer ' + key, Accept: 'application/json' },
      redirect: 'error', signal: combined,
    });
    if (!response.ok || !response.body) throw new Error();
    reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 1024 * 1024) throw new Error();
      chunks.push(Buffer.from(value));
    }
    const payload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!Array.isArray(payload.models)) throw new Error();
    for (const item of payload.models) {
      if (!item || typeof item.id !== 'string' || !item.id.trim() ||
          item.id !== 'auto' && (typeof item.stable_id !== 'string' || !item.stable_id.trim()) ||
          !Array.isArray(item.aliases ?? []) || !(item.aliases ?? []).every(v => typeof v === 'string') ||
          !Array.isArray(item.provider_refs ?? []) || !(item.provider_refs ?? []).every(ref =>
            ref && typeof ref.provider_id === 'string' && ref.provider_id.trim() && typeof ref.model_id === 'string' && ref.model_id.trim())) throw new Error();
    }
    signal?.throwIfAborted();
    return payload.models;
  } catch {
    if (signal?.aborted) throw new ManagedModelError('模型调用已取消', 'ABORTED');
    throw new ManagedModelError('模型授权目录暂不可用，请稍后重试；未切换其他模型', 'MODEL_CATALOG_UNAVAILABLE');
  } finally {
    clearTimeout(timer);
    await reader?.cancel().catch(() => {});
  }
}

function owned(profile) {
  return profile && typeof profile.apiKey === 'string' &&
    !!process.env.CLAWMANAGER_LLM_API_KEY?.trim() &&
    profile.apiKey === process.env.CLAWMANAGER_LLM_API_KEY.trim() &&
    baseOf(profile.baseUrl) === baseOf(process.env.CLAWMANAGER_LLM_BASE_URL);
}
function exact(item, provider, model) {
  return (item.provider_refs ?? []).some(ref => norm(ref.provider_id) === norm(provider) && norm(ref.model_id) === norm(model));
}

export async function resolveManagedReference(config, selection, signal) {
  if (!enabled()) return selection;
  signal?.throwIfAborted();
  const providers = config?.models?.providers ?? {};
  const registered = Object.hasOwn(providers, selection.provider);
  const profile = providers[selection.provider];
  // Configured custom providers always win. Missing arbitrary/built-in providers
  // cannot be claimed just because a historical leaf model happens to match.
  if (registered && !owned(profile)) return selection;
  if (!registered && selection.provider !== 'auto' && !/^cm-provider-v1-[a-f0-9]{64}$/.test(selection.provider ?? '')) return selection;
  const items = await readCatalog(signal);
  const hits = items.filter(item => exact(item, selection.provider, selection.model) ||
    (owned(profile) || selection.provider === 'auto') && namesOf(item).has(norm(selection.model)));
  if (hits.length !== 1) throw unavailable();
  const item = hits[0];
  const candidates = [];
  for (const [provider, candidate] of Object.entries(providers)) {
    if (!owned(candidate) || !Array.isArray(candidate.models)) continue;
    for (const model of candidate.models) {
      if (typeof model?.id !== 'string' || !namesOf(item).has(norm(model.id))) continue;
      // Gateway receives the bare native wire ID. It must still identify only
      // the exact same deployment (including across different upstream URLs).
      const wireHits = items.filter(entry => namesOf(entry).has(norm(model.id)));
      if (wireHits.length !== 1 || wireHits[0] !== item) continue;
      candidates.push({provider, model: model.id});
    }
  }
  candidates.sort((a,b) => Number(b.model === item.id) - Number(a.model === item.id) ||
    Number(b.provider === selection.provider) - Number(a.provider === selection.provider) ||
    a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model));
  const target = candidates[0];
  if (!target) throw unavailable();
  signal?.throwIfAborted();
  if (target.provider === selection.provider && target.model === selection.model) return selection;
  return {...selection, ...target};
}

export async function prepareManagedRun(params, configKey = 'config') {
  // Native harness ownership is a separate contract: never retarget its locked
  // session. Explicit auth-profile overrides likewise stay with native routing.
  if (!enabled() || params.modelSelectionLocked || params.authProfileId ||
      params.userLockedAuthProfileId || !params.provider || !params.model) return params;
  const selection = await resolveManagedReference(params[configKey], {provider:params.provider, model:params.model}, params.abortSignal);
  if (selection.provider === params.provider && selection.model === params.model) return params;
  return {...params, ...selection, requestedRouteResolution:'raw'};
}

// Narrow the native picker, never expand it: native account, operator and
// session policy decisions remain authoritative. Historical selection fields
// and transcripts are intentionally not rewritten or silently switched to auto.
export async function filterManagedModelCatalog(config, payload, signal, selection = {}) {
  if (!enabled() || !Array.isArray(payload?.models) || selection.modelSelectionLocked ||
      selection.authProfileId || selection.userLockedAuthProfileId) return payload;
  const providers = config?.models?.providers ?? {};
  const managed = row => {
    if (!row || typeof row.provider !== 'string') return false;
    if (Object.hasOwn(providers, row.provider)) return owned(providers[row.provider]);
    return row.provider === 'auto' || /^cm-provider-v1-[a-f0-9]{64}$/.test(row.provider);
  };
  if (!payload.models.some(managed)) return payload;
  const items = await readCatalog(signal);
  const models = payload.models.filter(row => {
    if (!managed(row)) return true;
    // A removed native provider/model is not a selectable route, even when its
    // history can be restored at dispatch. Do not synthesize an unconfigured one.
    const profile = providers[row.provider];
    if (!owned(profile) || !profile.models?.some(model => model.id === row.id)) return false;
    const hits = items.filter(item => namesOf(item).has(norm(row.id)) || exact(item, row.provider, row.id));
    // The bare wire ID must identify the same one deployment, not merely match
    // a historical pair belonging to another deployment on another upstream URL.
    return hits.length === 1 && namesOf(hits[0]).has(norm(row.id));
  });
  signal?.throwIfAborted();
  return {...payload, models};
}
