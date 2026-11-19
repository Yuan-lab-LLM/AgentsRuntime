// Runtime task protocol execution layer. The native integration MUST await all
// tools before its operation settles and MUST attach these headers to every
// Gateway request (including Auto, fallback and compaction). This module alone
// is not evidence that an OpenClaw native turn supports whole-turn cancellation.
import {createTaskReceiptStore} from './task_receipt_store.mjs';
export class RuntimeTaskError extends Error {
  constructor(code, revoked = false) {
    const messages = {
      MODEL_UNAVAILABLE: '当前模型不可用，本轮任务已中止，请重新选择模型',
      MODEL_AUTHORIZATION_UNAVAILABLE: '模型授权检查暂不可用，已停止当前任务以避免越权调用',
      INVALID_TASK_IDENTITY: '无法确认本轮任务身份，未开始执行',
      TASK_ALREADY_RUNNING: '本轮任务已在执行，不能重复启动',
      TASK_RECEIPT_PENDING: '历史任务停止状态或完成回执尚未确认，暂时不能开始新任务，请联系管理员检查',
      ABORTED: '本轮任务已取消',
    };
    super(messages[code] ?? messages.MODEL_AUTHORIZATION_UNAVAILABLE);
    this.name = 'ClawManagerRuntimeTaskError';
    this.code = code;
    this.revoked = revoked;
  }
}
const unavailable = () => new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
export class RuntimeTaskCleanupError extends RuntimeTaskError {
  constructor() { super('MODEL_AUTHORIZATION_UNAVAILABLE'); this.name='ClawManagerRuntimeTaskCleanupError'; }
}
const validID = value => typeof value === 'string' && /^[\x21-\x7e]{1,191}$/.test(value);

export function createRuntimeTaskGuard({baseURL, apiKey, generation, fetcher = globalThis.fetch,
  pollMs = 1000, timeoutMs = 2500, maxPendingReceipts = 128, receiptDirectory,
  receiptRetryMs=5000, receiptRetryMaxMs=60000, onReceiptStatus,
  scheduler={setTimeout,clearTimeout,now:Date.now}} = {}) {
  let base;
  try {
    const url = new URL(baseURL);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash ||
        typeof apiKey !== 'string' || !apiKey.trim() || /[\r\n]/.test(apiKey) ||
        !Number.isSafeInteger(generation) || generation < 0 || generation > 2147483647 ||
        !Number.isSafeInteger(pollMs) || pollMs < 1 ||
        !Number.isSafeInteger(timeoutMs) || timeoutMs < 1 ||
        !Number.isSafeInteger(maxPendingReceipts) || maxPendingReceipts < 1 ||
        !Number.isSafeInteger(receiptRetryMs) || receiptRetryMs<1 ||
        !Number.isSafeInteger(receiptRetryMaxMs) || receiptRetryMaxMs<receiptRetryMs) throw new Error();
    base = url.href.replace(/\/+$/, '');
  } catch { throw new RuntimeTaskError('INVALID_TASK_IDENTITY'); }
  const active = new Set(), receipts = new Map(), sending = new Map(), cleanupUnconfirmed = new Set();
  let store,temporaryFiles,unpublishedRecovery,recoveredExecutions=0;const storageFailures=new Set(),executionTokens=new Map();
  if(receiptDirectory!==undefined) {
    try {
      store=createTaskReceiptStore({directory:receiptDirectory,baseURL:base,apiKey,generation,limit:maxPendingReceipts});
      recoveredExecutions=store.pendingExecutions();
      if(recoveredExecutions)storageFailures.add('execution-recovery');
      // Keep recovery bounded and diagnostic failures separate from delivery.
      // Validate published state first so corruption cannot be hidden by recovery.
      store.list();
      try{unpublishedRecovery=store.recoverUnpublished();}catch{unpublishedRecovery=Object.freeze({errors:1});}
      for(const receipt of store.list())receipts.set(JSON.stringify({generation:receipt.generation,session_id:receipt.session_id,task_id:receipt.task_id}),receipt);
      // Best effort housekeeping must not prevent an authenticated receipt
      // from being delivered. Unknown/unpublished files remain untouched.
      try{temporaryFiles=store.maintainTemporaryFiles();}catch{temporaryFiles=Object.freeze({errors:1});}
    } catch {try{store?.closeRecovery();}catch{}throw new RuntimeTaskError('TASK_RECEIPT_PENDING');}
  }
  let flush;
  let recoveryPending=Boolean(unpublishedRecovery?.truncated||unpublishedRecovery?.capacityBlocked||unpublishedRecovery?.errors&&!('scanned' in unpublishedRecovery));
  let retryTimer,retrying=false,retriesStopped=false,consecutiveFailures=0,lastErrorCode=null,lastAttemptAt=null,nextRetryAt=null;
  function receiptStatus(){return Object.freeze({pending:receipts.size,active:active.size,cleanupUnconfirmed:cleanupUnconfirmed.size,recoveredExecutions,storageBlocked:storageFailures.size>0,consecutiveFailures,lastErrorCode:recoveredExecutions?'TASK_EXECUTION_RECOVERY_REQUIRED':cleanupUnconfirmed.size?'TASK_CLEANUP_UNCONFIRMED':lastErrorCode,lastAttemptAt,nextRetryAt,retriesStopped,temporaryFiles,unpublishedRecovery,recoveryPending});}
  function publish(){try{onReceiptStatus?.(receiptStatus());}catch{/* Diagnostics must not alter task authorization. */}}
  function scheduleRetry(){
    const work=!recoveredExecutions&&(receipts.size>0||recoveryPending);
    if(retryTimer!==undefined && (!work||retriesStopped)){scheduler.clearTimeout(retryTimer);retryTimer=undefined;nextRetryAt=null;}
    if(!work||retryTimer!==undefined||retrying||retriesStopped)return;
    const delay=Math.min(receiptRetryMaxMs,receiptRetryMs*2**Math.min(10,Math.max(0,consecutiveFailures-1)));
    nextRetryAt=scheduler.now()+delay;
    retryTimer=scheduler.setTimeout(async()=>{
      retryTimer=undefined;nextRetryAt=null;retrying=true;
      try{await flushReceipts();}finally{retrying=false;scheduleRetry();publish();}
    },delay);
    retryTimer?.unref?.();
  }
  function stopReceiptRetries(){retriesStopped=true;store?.closeRecovery();scheduleRetry();publish();}
  function continueRecovery(){
    // Never consume slots reserved by a running task, or replace an in-flight
    // receipt object. Admission pauses until the bounded recovery work drains.
    if(!store||!recoveryPending||active.size||receipts.size||sending.size)return;
    try{
      unpublishedRecovery=store.recoverUnpublished();
      for(const receipt of store.list())receipts.set(JSON.stringify({generation:receipt.generation,session_id:receipt.session_id,task_id:receipt.task_id}),receipt);
      recoveryPending=unpublishedRecovery.truncated||unpublishedRecovery.capacityBlocked;
      storageFailures.delete('recovery');
      if(lastErrorCode==='RECEIPT_RECOVERY_UNAVAILABLE'){consecutiveFailures=0;lastErrorCode=null;}
    }catch{
      recoveryPending=true;storageFailures.add('recovery');consecutiveFailures++;lastErrorCode='RECEIPT_RECOVERY_UNAVAILABLE';
    }
  }
  async function request(path, body, signal) {
    const deadline = new AbortController();
    const timer = setTimeout(() => deadline.abort(), timeoutMs);
    const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
    let reader;
    try {
      combined.throwIfAborted();
      const response = await fetcher(`${base}/runtime/${path}`, {
        method: 'POST', redirect: 'error', signal: combined,
        headers: {Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json', Accept: 'application/json'},
        body: JSON.stringify(body),
      });
      if (response.status === 403) throw new RuntimeTaskError('MODEL_UNAVAILABLE', true);
      if (!response.ok || !response.body) throw unavailable();
      reader = response.body.getReader();
      const chunks = []; let size = 0;
      for (;;) {
        const {done, value} = await reader.read();
        if (done) break;
        size += value.byteLength;
        if (size > 4096) throw unavailable();
        chunks.push(Buffer.from(value));
      }
      combined.throwIfAborted();
      return JSON.parse(Buffer.concat(chunks).toString('utf8'));
    } catch (error) {
      if (signal?.aborted) throw new RuntimeTaskError('ABORTED');
      if (error instanceof RuntimeTaskError) throw error;
      throw unavailable();
    } finally {
      clearTimeout(timer);
      void reader?.cancel().catch(() => {});
    }
  }
  async function check(identity, signal) {
    const state = await request('tasks/check', identity, signal);
    if (!state || typeof state.revoked !== 'boolean' || typeof state.finished !== 'boolean') throw unavailable();
    if (state.revoked || state.finished) throw new RuntimeTaskError('MODEL_UNAVAILABLE', state.revoked);
  }
  function sendReceipt(id, receipt) {
    if (sending.has(id)) return sending.get(id);
    const pending = (async () => {
      lastAttemptAt=scheduler.now();
      try {
        try {
          store?.put(receipt);
          if(executionTokens.has(id)){store.completeExecution(executionTokens.get(id),receipt);executionTokens.delete(id);}
          storageFailures.delete(id);
        }catch{storageFailures.add(id);throw unavailable();}
        const result = await request('tasks/finish', receipt);
        if (result?.accepted !== true) throw unavailable();
        try{store?.remove(receipt);}catch{storageFailures.add(id);throw unavailable();}
        if (receipts.get(id) === receipt) receipts.delete(id);
        consecutiveFailures=0;lastErrorCode=null;
        return true;
      } catch {consecutiveFailures++;lastErrorCode=storageFailures.has(id)?'RECEIPT_STORAGE_UNAVAILABLE':'RECEIPT_DELIVERY_UNCONFIRMED';return false;}
    })().finally(() => { sending.delete(id);scheduleRetry();publish(); });
    sending.set(id, pending);
    return pending;
  }
  async function flushReceipts() {
    // A previous process left uncertain execution state. Even a coexisting
    // receipt is not permission to automatically reconcile or replay it.
    if(recoveredExecutions)return;
    if (flush) return flush;
    // Bounded work per pass; background retries stop once the bounded queue drains.
    flush = (async () => {
      continueRecovery();
      for (const [id, receipt] of [...receipts].slice(0, 8)) {
        if (!await sendReceipt(id, receipt)) break;
      }
    })().finally(() => { flush = undefined;scheduleRetry();publish(); });
    return flush;
  }
  async function run({sessionId, taskId, signal} = {}, operation) {
    if (!validID(sessionId) || !validID(taskId) || typeof operation !== 'function') throw new RuntimeTaskError('INVALID_TASK_IDENTITY');
    if (signal?.aborted) throw new RuntimeTaskError('ABORTED');
    if(recoveryPending){await flushReceipts();if(recoveryPending)throw new RuntimeTaskError('TASK_RECEIPT_PENDING');}
    const identity = Object.freeze({generation, session_id: sessionId, task_id: taskId});
    const id = JSON.stringify(identity);
    if (active.has(id)) throw new RuntimeTaskError('TASK_ALREADY_RUNNING');
    // Reserve before the first await: concurrent starts cannot race admission.
    active.add(id);
    try {
      if (storageFailures.size || receipts.has(id) || receipts.size + active.size > maxPendingReceipts) await flushReceipts();
      if (storageFailures.size || receipts.has(id) || receipts.size + active.size > maxPendingReceipts) throw new RuntimeTaskError('TASK_RECEIPT_PENDING');
      const controller = new AbortController(), watchStop = new AbortController();
      const operationSignal = signal ? AbortSignal.any([signal, controller.signal]) : controller.signal;
      const watchSignal = signal ? AbortSignal.any([signal, watchStop.signal]) : watchStop.signal;
      let terminal, admitted = false, settled = false, cleanupConfirmed = true, watch, pollTimer, wake;
      const stop = error => {
        if (settled || terminal) return;
        terminal = error;
        controller.abort(error);
      };
      const onAbort = () => { stop(new RuntimeTaskError('ABORTED')); wake?.(); };
      signal?.addEventListener('abort', onAbort, {once: true});
      try {
        // No model/tool dispatch before this exact task is accepted by Gateway.
        await check(identity, operationSignal);
        admitted = true;
        operationSignal.throwIfAborted();
        if(store){
          try{executionTokens.set(id,store.beginExecution(identity));}
          catch{cleanupConfirmed=false;throw new RuntimeTaskError('TASK_RECEIPT_PENDING');}
        }
        watch = (async () => {
          while (!settled && !terminal) {
            await new Promise(resolve => { wake = resolve; pollTimer = setTimeout(resolve, pollMs); });
            clearTimeout(pollTimer); wake = undefined;
            if (settled || terminal) break;
            try { await check(identity, watchSignal); }
            catch (error) { if (!settled) stop(error); }
          }
        })();
        const headers = Object.freeze({
          'X-ClawManager-Session-ID': sessionId,
          'X-ClawManager-Task-ID': taskId,
          'X-ClawManager-Runtime-Generation': String(generation),
        });
        // Deliberately NOT Promise.race: cancellation must join native tools
        // before a receipt can claim this execution has stopped.
        try { return await operation({signal: operationSignal, headers}); }
        finally { settled = true; }
      } catch (error) {
        if(error instanceof RuntimeTaskCleanupError)cleanupConfirmed=false;
        if (!terminal) throw error;
      } finally {
        settled = true;
        clearTimeout(pollTimer); wake?.(); watchStop.abort();
        signal?.removeEventListener('abort', onAbort);
        await watch;
        if(admitted && cleanupConfirmed) {
          const receipt = Object.freeze({...identity, cancelled: terminal?.revoked === true});
          receipts.set(id, receipt);
          await sendReceipt(id, receipt);
        } else if(admitted) {
          // Failed cleanup is neither completion nor cancellation evidence.
          // Keep the pre-dispatch journal on disk and block this process.
          // Restart blocks across credentials/generations until reconciliation.
          cleanupUnconfirmed.add(id);storageFailures.add('cleanup:'+id);
          consecutiveFailures++;lastErrorCode='TASK_CLEANUP_UNCONFIRMED';publish();
        }
        if (terminal) throw terminal;
      }
    } finally {
      active.delete(id);
    }
  }
  scheduleRetry();publish();
  return Object.freeze({run, flushReceipts,receiptStatus,stopReceiptRetries,get pendingReceipts() { return receipts.size; }});
}
