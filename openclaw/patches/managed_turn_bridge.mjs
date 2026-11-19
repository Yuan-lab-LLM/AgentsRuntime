import {AsyncLocalStorage} from 'node:async_hooks';
import {randomUUID} from 'node:crypto';
import {createRuntimeTaskGuard, RuntimeTaskError, RuntimeTaskCleanupError} from './runtime_task_guard.mjs';
import {createCapabilityRenewal,capabilityRenewalReleaseAccepted} from './runtime_capability_renewal.mjs';
import {acquireRuntimeWriter} from './runtime_writer_guard.mjs';
import {createShutdownReceiptStore} from './shutdown_receipt_store.mjs';
const scope = new AsyncLocalStorage();
const baseOf = value => typeof value === 'string' ? value.trim().replace(/\/+$/, '') : '';
const profileOwned = (profile, base, key) => !!key && profile?.apiKey === key && baseOf(profile.baseUrl) === base;
let runtime;
let writer;
let draining=false,nativeStopped=false,shutdown;
function managedIdle(){
 const s=runtime?.guard.receiptStatus();
 return Boolean(s)&&s.active===0&&s.pending===0&&s.cleanupUnconfirmed===0&&s.recoveredExecutions===0&&s.storageBlocked===false&&s.recoveryPending===false;
}
function assertRuntimeWriter(){
 if(!writer)return;
 try{writer.assertOwned();}catch{runtime?.renewal?.stop('IDENTITY_CHANGED');throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');}
}
function settings() {
  return {base:baseOf(process.env.CLAWMANAGER_LLM_BASE_URL),key:process.env.CLAWMANAGER_LLM_API_KEY?.trim(),generation:process.env.CLAWMANAGER_GATEWAY_GENERATION,receiptDirectory:process.env.CLAWMANAGER_TASK_RECEIPT_DIR};
}
function guardFor(config) {
  // Do not abandon pending receipts or mix owners after a process-local env change.
  if(runtime && (runtime.base!==config.base || runtime.key!==config.key || runtime.generation!==config.generation || runtime.receiptDirectory!==config.receiptDirectory)) {
    runtime.renewal?.stop('IDENTITY_CHANGED');
    throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
  }
  if(!runtime) {
    if(!/^(0|[1-9][0-9]*)$/.test(config.generation??'')) throw new RuntimeTaskError('INVALID_TASK_IDENTITY');
    if(capabilityRenewalReleaseAccepted&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL==='1') {
      // No exit-time auto-release. Failed initialization leaves evidence.
      try{writer=acquireRuntimeWriter({directory:config.receiptDirectory,generation:Number(config.generation),isQuiescent:()=>draining&&nativeStopped&&managedIdle()});}
      catch{throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');}
    }
    let warned=false,lastWarning=0;
    runtime={...config,guard:createRuntimeTaskGuard({baseURL:config.base,apiKey:config.key,generation:Number(config.generation),receiptDirectory:config.receiptDirectory,onReceiptStatus:status=>{
      if((status.consecutiveFailures>0||status.storageBlocked) && (!warned||Date.now()-lastWarning>=60000)) {
        console.warn('[ClawManager] task receipt pending',JSON.stringify({pending:status.pending,storageBlocked:status.storageBlocked,error:status.lastErrorCode}));warned=true;lastWarning=Date.now();
      } else if(warned&&status.pending===0&&!status.storageBlocked&&!status.recoveryPending&&!status.cleanupUnconfirmed){console.info('[ClawManager] task receipt queue recovered');warned=false;}
    }})};
    runtime.renewal=createCapabilityRenewal({
      allowEnrollment:process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT==='1',
      canEnroll:()=>{
        assertRuntimeWriter();
        const status=runtime.guard.receiptStatus();
        return Boolean(config.receiptDirectory)&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT==='1'&&
          status.pending===0&&status.active===0&&status.cleanupUnconfirmed===0&&status.recoveredExecutions===0&&
          status.storageBlocked===false&&status.recoveryPending===false;
      },
      enabled:process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL==='1',
      releaseAccepted:capabilityRenewalReleaseAccepted,
      baseURL:config.base,apiKey:config.key,
      report:{protocol:1,generation:Number(config.generation),runtime_version:'2026.10.2',adapter_version:'cm-openclaw-native-1',capabilities:['task_identity','turn_cancel','cancel_receipt','live_catalog']},
      isCurrent:()=>{assertRuntimeWriter();const now=settings();return process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL==='1'&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL==='1'&&now.base===config.base&&now.key===config.key&&now.generation===config.generation&&now.receiptDirectory===config.receiptDirectory;},
    });
    void runtime.renewal.start();
    // Preserve native signal handlers; shutdown only stops this process's loop.
    if(capabilityRenewalReleaseAccepted&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL==='1')process.once('exit',stopManagedCapabilityRenewal);
  }
  assertRuntimeWriter();
  return runtime.guard;
}
function assertOpen(current) {
  assertRuntimeWriter();
  if(current.closed) throw new RuntimeTaskError('MODEL_UNAVAILABLE');
  if(current.signal.aborted) throw current.signal.reason instanceof RuntimeTaskError ? current.signal.reason : new RuntimeTaskError('ABORTED');
}
function invoke(current, params, operation) {
  assertOpen(current);
  const signal=params.abortSignal ? AbortSignal.any([params.abortSignal,current.signal]) : current.signal;
  const pending=Promise.resolve().then(()=>{
    assertOpen(current);
    signal.throwIfAborted();
    return operation({...params,abortSignal:signal});
  });
  current.pending.add(pending);
  void pending.then(()=>current.pending.delete(pending),error=>{
    current.pending.delete(pending);
    if(error instanceof RuntimeTaskError)current.failures.push(error);
  });
  return pending;
}
// One outer native fallback/run scope owns the receipt. Nested candidates,
// embedded steps and compaction inherit it via async context, never a global
// session lookup. Descendants are joined; late detached work fails closed.
export async function withManagedTurn(params, operation, configKey='config', kind='turn') {
  const current=scope.getStore();
  if(current)return invoke(current,params,operation);
  if(draining)throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
  if(process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL!=='1')return operation(params);
  const config=settings(),providers=params[configKey]?.models?.providers??{};
  if(params.modelSelectionLocked || params.authProfileId || params.userLockedAuthProfileId)return operation(params);
  if(!profileOwned(providers[params.provider],config.base,config.key)) {
    if(!params.provider && Object.values(providers).some(p=>profileOwned(p,config.base,config.key))) throw new RuntimeTaskError('INVALID_TASK_IDENTITY');
    return operation(params);
  }
  const guard=guardFor(config);
  // In the future accepted enrollment mode no native model/tool work may
  // race first registration. Legacy/default mode retains its existing path.
  if(capabilityRenewalReleaseAccepted&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT==='1') {
    await runtime.renewal.start();
    if(runtime.renewal.status().state!=='active')throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
  }
  // Shutdown may have started while asynchronous startup negotiation waited.
  if(draining)throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
  // No guessed accounting fallback. A standalone manual compaction is a new
  // operation; automatic compaction inherits the enclosing task above.
  const sessionId=params.sessionId || params.sessionKey;
  const taskId=params.runId || (kind==='compaction' ? 'compaction:'+randomUUID() : undefined);
  void guard.flushReceipts();
  return guard.run({sessionId,taskId,signal:params.abortSignal},async({signal,headers})=>{
    const state={...config,signal,headers,pending:new Set(),failures:[],closed:false};
    return scope.run(state,async()=>{
      try {return await invoke(state,params,operation);}
      finally {
        while(state.pending.size)await Promise.allSettled([...state.pending]);
        state.closed=true;
        if(state.failures.length)throw state.failures.find(e=>e instanceof RuntimeTaskCleanupError) ?? state.failures[0];
      }
    });
  });
}
export function attachManagedTaskHeaders(result, config, options) {
  const current=scope.getStore();
  if(!current || !result?.model)return result;
  assertOpen(current);
  const provider=result.logicalRef?.provider ?? result.model.provider;
  const profile=config?.models?.providers?.[provider];
  if(!profileOwned(profile,current.base,current.key))return result;
  // Never leak a task identity to a native/auth-profile transport override.
  if(options?.authProfileId || options?.userLockedAuthProfileId || baseOf(result.model.baseUrl)!==current.base) throw new RuntimeTaskError('MODEL_AUTHORIZATION_UNAVAILABLE');
  const headers={};
  for(const [key,value] of Object.entries(result.model.headers??{})) {
    if(!Object.keys(current.headers).some(h=>h.toLowerCase()===key.toLowerCase()))headers[key]=value;
  }
  return {...result,model:{...result.model,headers:{...headers,...current.headers}}};
}
export async function joinManagedCleanup(result, owner) {
  if(!scope.getStore())return result;
  try {return await result;} finally {
    try {await owner;} catch {throw new RuntimeTaskCleanupError();}
  }
}
export const hasManagedTurn = () => Boolean(scope.getStore());
export const managedTaskReceiptStatus=()=>runtime?.guard.receiptStatus() ?? {initialized:false};
export const managedCapabilityRenewalStatus=()=>runtime?.renewal?.status() ?? {state:'disabled'};
export const stopManagedCapabilityRenewal=()=>runtime?.renewal?.stop();
// Internal native-controller API, not an untrusted HTTP/CLI approval switch.
// The native controller must first stop all native work, including custom
// routes outside managed accounting. This module cannot infer that fact.
export function beginManagedShutdown(){
 draining=true;
 return Object.freeze({draining:true,managed_idle:managedIdle(),receipt_status:managedTaskReceiptStatus()});
}
export function finishManagedShutdown({confirmNativeStopped=false,verifyNativeStopped=()=>true}={}){
 if(scope.getStore()||!draining||confirmNativeStopped!==true||!runtime)return Promise.reject(new RuntimeTaskError('SHUTDOWN_CONFIRMATION_REQUIRED'));
 if(shutdown)return shutdown;
 shutdown=(async()=>{
  await runtime.guard.flushReceipts();
  if(typeof verifyNativeStopped!=='function'||verifyNativeStopped()!==true)throw new RuntimeTaskError('SHUTDOWN_CONFIRMATION_REQUIRED');
  if(!managedIdle())throw new RuntimeTaskError('TASK_RECEIPT_PENDING');
  assertRuntimeWriter();
  nativeStopped=true;
  runtime.renewal?.stop();
  runtime.guard.stopReceiptRetries();
  if(writer) {
   // Durable signed cleanup evidence precedes unlock. Publication failure
   // retains the writer barrier; this receipt does not claim release succeeded.
   const store=createShutdownReceiptStore({directory:runtime.receiptDirectory,baseURL:runtime.base,apiKey:runtime.key,generation:Number(runtime.generation)});
   store.write(writer.claimID);
  }
  // Synchronous final ownership/quiescence check and release. A failed
  // release stays blocked; do not resume tasks or delete foreign evidence.
  writer?.release();
  return Object.freeze({draining:true,released:Boolean(writer)});
 })();
 const attempt=shutdown;
 void attempt.catch(()=>{if(shutdown===attempt)shutdown=undefined;});
 return attempt;
}
// Restore a persistent queue when the patched module loads, even if no new
// user turn arrives. Default/legacy mode performs no I/O and starts no timers.
if(process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL==='1'&&process.env.CLAWMANAGER_TASK_RECEIPT_DIR){
  try{guardFor(settings());}catch{console.warn('[ClawManager] task receipt recovery unavailable; managed task admission remains fail-closed');}
}
