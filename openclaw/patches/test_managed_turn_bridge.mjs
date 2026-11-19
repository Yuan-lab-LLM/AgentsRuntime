import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {AsyncLocalStorage} from 'node:async_hooks';
import {contracts,transform} from './patch_managed_model_reference_102.mjs';
import {RuntimeTaskError} from './runtime_task_guard.mjs';
const base='http://gateway.invalid/v1',key='test-key';
const cfg={models:{providers:{owned:{baseUrl:base,apiKey:key,models:[{id:'model'}]},foreign:{baseUrl:base,apiKey:'custom',models:[{id:'model'}]}}}};
const params={config:cfg,provider:'owned',model:'model',sessionId:'s',runId:'t'};
const resolved=()=>({logicalRef:{provider:'owned',model:'model'},model:{provider:'owned',id:'model',baseUrl:base,headers:{'x-clawmanager-task-id':'forged','X-Test':'preserve'}}});
const deferred=()=>{let resolve;const promise=new Promise(r=>{resolve=r;});return {resolve,promise};};
let originalEnv,originalFetch,bridge,calls,revoked,finishHook,index=0;
test.beforeEach(async()=>{
 originalEnv={...process.env};originalFetch=globalThis.fetch;calls=[];revoked=false;finishHook=undefined;
 Object.assign(process.env,{CLAWMANAGER_OPENCLAW_TASK_PROTOCOL:'1',CLAWMANAGER_LLM_BASE_URL:base,CLAWMANAGER_LLM_API_KEY:key,CLAWMANAGER_GATEWAY_GENERATION:'7'});
 globalThis.fetch=async(url,options)=>{
  const body=JSON.parse(options.body);calls.push({url,body});
  if(url.endsWith('/check'))return revoked?new Response('',{status:403}):Response.json({revoked:false,finished:false});
  assert.ok(url.endsWith('/finish'));finishHook?.(body);return Response.json({accepted:true});
 };
 bridge=await import('./managed_turn_bridge.mjs?test='+(++index));
});
test.afterEach(()=>{process.env=originalEnv;globalThis.fetch=originalFetch;});
test('outer turn, retry and compaction reuse one identity and one receipt',async()=>{
 const result=await bridge.withManagedTurn(params,async outer=>{
  assert.ok(outer.abortSignal);const first=bridge.attachManagedTaskHeaders(resolved(),cfg);
  assert.equal(first.model.headers['X-ClawManager-Task-ID'],'t');assert.equal(first.model.headers['x-clawmanager-task-id'],undefined);
  assert.equal(first.model.headers['X-Test'],'preserve');
  for(const runId of ['retry-2',undefined])await bridge.withManagedTurn({...params,runId},async()=>{
   assert.equal(bridge.attachManagedTaskHeaders(resolved(),cfg).model.headers['X-ClawManager-Task-ID'],'t');
  },'config','compaction');
  return 'ok';
 });
 assert.equal(result,'ok');assert.equal(calls.filter(c=>c.url.endsWith('/finish')).length,1);
 assert.ok(calls.every(c=>c.body.task_id==='t'&&c.body.session_id==='s'));
});
test('drain rejects new outer work but joins existing descendants and receipts',async()=>{
 const entered=deferred(),release=deferred();let nested=false;
 const running=bridge.withManagedTurn(params,async()=>{
  entered.resolve();await release.promise;
  await bridge.withManagedTurn({...params,runId:'nested'},async()=>{nested=true;});
 });
 await entered.promise;
 assert.equal(bridge.beginManagedShutdown().managed_idle,false);
 await assert.rejects(bridge.withManagedTurn({...params,runId:'new'},async()=>assert.fail('new work admitted')));
 await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:true}),e=>e.code==='TASK_RECEIPT_PENDING');
 release.resolve();await running;assert.equal(nested,true);
 assert.equal(bridge.beginManagedShutdown().managed_idle,true);
 assert.deepEqual(await bridge.finishManagedShutdown({confirmNativeStopped:true}),{draining:true,released:false});
 assert.equal(calls.filter(c=>c.url.endsWith('/finish')).length,1);
 await assert.rejects(bridge.withManagedTurn({...params,runId:'later'},async()=>assert.fail('resumed after shutdown')));
});
test('release requires explicit shutdown stage and native confirmation',async()=>{
 await bridge.withManagedTurn(params,async()=>{});
 await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:true}),e=>e.code==='SHUTDOWN_CONFIRMATION_REQUIRED');
 bridge.beginManagedShutdown();
 await assert.rejects(bridge.finishManagedShutdown(),e=>e.code==='SHUTDOWN_CONFIRMATION_REQUIRED');
 await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:'true'}),e=>e.code==='SHUTDOWN_CONFIRMATION_REQUIRED');
});
test('native cleanup proof is rechecked at final release boundary',async()=>{
 await bridge.withManagedTurn(params,async()=>{});bridge.beginManagedShutdown();
 await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:true,verifyNativeStopped:()=>false}),e=>e.code==='SHUTDOWN_CONFIRMATION_REQUIRED');
 await assert.rejects(bridge.withManagedTurn({...params,runId:'later'},async()=>assert.fail('drain was lost')));
});
test('shutdown confirmation cannot originate inside the active native turn',async()=>{
 await bridge.withManagedTurn(params,async()=>{
  bridge.beginManagedShutdown();
  await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:true}),e=>e.code==='SHUTDOWN_CONFIRMATION_REQUIRED');
 });
 await bridge.finishManagedShutdown({confirmNativeStopped:true});
});
test('unconfirmed cleanup retains draining state and denies release',async()=>{
 const {RuntimeTaskCleanupError}=await import('./runtime_task_guard.mjs');
 await assert.rejects(bridge.withManagedTurn(params,async()=>{throw new RuntimeTaskCleanupError();}));
 bridge.beginManagedShutdown();
 await assert.rejects(bridge.finishManagedShutdown({confirmNativeStopped:true}),e=>e.code==='TASK_RECEIPT_PENDING');
 await assert.rejects(bridge.withManagedTurn({...params,runId:'later'},async()=>assert.fail('resumed after uncertain cleanup')));
});
test('explicit renewal opt-in cannot bypass the native release gate',async()=>{
 process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT='1';
 process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL='1';
 await bridge.withManagedTurn(params,async()=>{});
 assert.equal(bridge.managedCapabilityRenewalStatus().state,'release_blocked');
 assert.equal(bridge.managedCapabilityRenewalStatus().attempts,0);
 assert.ok(calls.every(c=>!c.url.includes('/capabilities')));
 bridge.stopManagedCapabilityRenewal();assert.equal(bridge.managedCapabilityRenewalStatus().state,'stopped');
});
test('legacy protocol ignores renewal opt-in and makes no capability requests',async()=>{
 process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL='0';process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL='1';
 await bridge.withManagedTurn(params,async()=>{});assert.equal(calls.length,0);assert.equal(bridge.managedCapabilityRenewalStatus().state,'disabled');
});
test('parallel sessions do not borrow each other task headers',async()=>{
 const ready=deferred(),release=deferred();
 const a=bridge.withManagedTurn(params,async()=>{ready.resolve();await release.promise;return bridge.attachManagedTaskHeaders(resolved(),cfg).model.headers;});
 await ready.promise;
 const b=await bridge.withManagedTurn({...params,sessionId:'s2',runId:'t2'},async()=>bridge.attachManagedTaskHeaders(resolved(),cfg).model.headers);
 release.resolve();assert.equal((await a)['X-ClawManager-Task-ID'],'t');assert.equal(b['X-ClawManager-Task-ID'],'t2');
});
test('native descendants are joined before finish and late detached work is rejected',async()=>{
 const ready=deferred(),release=deferred(),late=deferred();let lateCall;
 const running=bridge.withManagedTurn(params,async()=>{
  void bridge.withManagedTurn({...params,runId:'child'},async()=>{ready.resolve();await release.promise;});
  lateCall=late.promise.then(()=>bridge.withManagedTurn(params,async()=>{throw new Error('must not dispatch');}));
 });
 await ready.promise;assert.equal(calls.some(c=>c.url.endsWith('/finish')),false);
 release.resolve();await running;
 late.resolve();await assert.rejects(lateCall,e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(calls.filter(c=>c.url.endsWith('/finish')).length,1);
});
test('revoke aborts nested tool work before outer receipt',{timeout:4000},async()=>{
 let stopped=false;
 finishHook=body=>{assert.equal(stopped,true);assert.equal(body.cancelled,true);};
 await assert.rejects(bridge.withManagedTurn(params,async()=>{
  return bridge.withManagedTurn({...params,runId:'child'},async({abortSignal})=>{
   revoked=true;
   await new Promise(resolve=>abortSignal.addEventListener('abort',resolve,{once:true}));
   stopped=true;return 'swallowed cancellation';
  });
 }),e=>e.code==='MODEL_UNAVAILABLE');
});
test('disabled bridge and custom or locked routes remain untouched',async()=>{
 for(const extra of [{provider:'foreign'},{modelSelectionLocked:true},{authProfileId:'personal'}]) {
  const input={...params,...extra};assert.equal(await bridge.withManagedTurn(input,async p=>p),input);
 }
 process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL='0';assert.equal(await bridge.withManagedTurn(params,async p=>p),params);
 assert.equal(calls.length,0);
});
test('missing generation or turn identity fails before dispatch',async()=>{
 for(const runId of [undefined,'', 'bad\nheader'])await assert.rejects(bridge.withManagedTurn({...params,runId},async()=>{throw new Error('dispatch');}),e=>e.code==='INVALID_TASK_IDENTITY');
 assert.equal(calls.length,0);
});
test('standalone manual compaction uses a fresh explicit operation ID',async()=>{
 await bridge.withManagedTurn({...params,runId:undefined},async()=>{},'config','compaction');
 assert.match(calls[0].body.task_id,/^compaction:[a-f0-9-]+$/);
});
test('headers never mutate native models or leak to custom transports',async()=>{
 const model=resolved(),before=JSON.stringify(model);
 await bridge.withManagedTurn(params,async()=>{
  bridge.attachManagedTaskHeaders(model,cfg);assert.equal(JSON.stringify(model),before);
  const foreign={...model,logicalRef:{provider:'foreign'}};assert.equal(bridge.attachManagedTaskHeaders(foreign,cfg),foreign);
  assert.throws(()=>bridge.attachManagedTaskHeaders({...model,model:{...model.model,baseUrl:'http://foreign.invalid'}},cfg),e=>e.code==='MODEL_AUTHORIZATION_UNAVAILABLE');
  assert.throws(()=>bridge.attachManagedTaskHeaders(model,cfg,{authProfileId:'other'}),e=>e.code==='MODEL_AUTHORIZATION_UNAVAILABLE');
 });
});
test('changed owner credentials cannot silently discard pending task state',async()=>{
 await bridge.withManagedTurn(params,async()=>{});
 process.env.CLAWMANAGER_LLM_API_KEY='rotated';
 const changed={...params,config:{models:{providers:{owned:{...cfg.models.providers.owned,apiKey:'rotated'}}}}};
 await assert.rejects(bridge.withManagedTurn(changed,async()=>{}),e=>e.code==='MODEL_AUTHORIZATION_UNAVAILABLE');
});
test('native compaction cleanup is joined only inside a managed turn',async()=>{
 const cleanup=deferred();
 assert.equal(await bridge.joinManagedCleanup(Promise.resolve('legacy'),cleanup.promise),'legacy');
 const ready=deferred();
 const running=bridge.withManagedTurn(params,async()=>{ready.resolve();return bridge.joinManagedCleanup(Promise.resolve('managed'),cleanup.promise);});
 await ready.promise;assert.equal(calls.some(c=>c.url.endsWith('/finish')),false);
 cleanup.resolve();assert.equal(await running,'managed');assert.equal(calls.filter(c=>c.url.endsWith('/finish')).length,1);
});
test('missing generation is not silently treated as generation zero',async()=>{
 delete process.env.CLAWMANAGER_GATEWAY_GENERATION;
 await assert.rejects(bridge.withManagedTurn(params,async()=>{}),e=>e.code==='INVALID_TASK_IDENTITY');assert.equal(calls.length,0);
});
const fixture=process.env.OPENCLAW_NATIVE_FIXTURE_DIR;
test('actual native compaction attempt joins cleanup before task receipt',{skip:!fixture,timeout:4000},async()=>{
 const contract=contracts.find(c=>c.file.startsWith('compact-'));
 const source=transform(fs.readFileSync(path.join(fixture,contract.file),'utf8'),contract);
 const start=source.indexOf('async function compactEmbeddedAgentSessionDirectOnce('),end=source.indexOf('\n//#endregion',start);
 assert.ok(start>=0&&end>start);const wrapped=source.slice(start,end);
 const cleaning=deferred(),release=deferred();let restored=false;
 class Work {
  signal=new AbortController().signal;
  run(fn){return fn();} track(fn){return fn();} beginClose(){} async drain(){}
  static runWhenAllIdle(_scopes,fn){return fn();}
 }
 let failCleanup=false;
 const deps={createDeferredCore:()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};},
  captureAsyncWorkTracker:()=>fn=>fn(),getAsyncWorkSignal:()=>undefined,AsyncWorkScope:Work,AsyncLocalStorage,
  prepareDirectCompactionAttempt:async()=>({ok:true,value:{fail:()=>{throw new Error('terminal error swallowed');}}}),
  buildPreparedCompactionRuntime:async(_value,onCleanup)=>{onCleanup({restoreSkillEnvironment(){restored=true;},async disposeToolRuntimes(){cleaning.resolve();await release.promise;if(failCleanup)throw new Error('cleanup failed');}});return {};},
  executePreparedCompactionSession:async()=>({ok:true}),cmIsManagedModelError:e=>e instanceof RuntimeTaskError,hasModelFallbackStop:()=>false,
  formatErrorMessage:e=>e.message,cmJoinManagedCleanup:bridge.joinManagedCleanup,cmHasManagedTurn:bridge.hasManagedTurn};
 const once=new Function(...Object.keys(deps),'return ('+wrapped+')')(...Object.values(deps));
 const running=bridge.withManagedTurn(params,once);
 await cleaning.promise;assert.equal(restored,true);assert.equal(calls.some(c=>c.url.endsWith('/finish')),false);
 release.resolve();assert.deepEqual(await running,{ok:true});assert.equal(calls.filter(c=>c.url.endsWith('/finish')).length,1);
 failCleanup=true;
 await assert.rejects(bridge.withManagedTurn({...params,runId:'cleanup-failure'},once),e=>e.name==='ClawManagerRuntimeTaskCleanupError');
 assert.equal(calls.filter(c=>c.url.endsWith('/finish')&&c.body.task_id==='cleanup-failure').length,0);
 assert.equal(bridge.managedTaskReceiptStatus().cleanupUnconfirmed,1);
 const previousInfo=console.info,infos=[];console.info=(...args)=>infos.push(args);
 try{
  await assert.rejects(bridge.withManagedTurn({...params,runId:'after-cleanup-failure'},once),e=>e.code==='TASK_RECEIPT_PENDING');
  assert.equal(infos.length,0,'blocked cleanup must not log queue recovery');
 }finally{console.info=previousInfo;}
});
