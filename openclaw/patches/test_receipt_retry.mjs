import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {createRuntimeTaskGuard} from './runtime_task_guard.mjs';
import {createTaskReceiptStore} from './task_receipt_store.mjs';
function clock(){let now=100;const timers=new Map();let index=0;return {timers,now:()=>now,setTimeout(fn,delay){const id=++index;timers.set(id,{fn,delay});return id;},clearTimeout(id){timers.delete(id);},async tick(){const [id,{fn,delay}]=timers.entries().next().value;timers.delete(id);now+=delay;await fn();}};}
const config={baseURL:'http://gateway.invalid/v1',apiKey:'test-secret',generation:7};
function unpublish(directory){const root=path.join(directory,fs.readdirSync(directory)[0]),file=fs.readdirSync(root).find(n=>n.endsWith('.json'));fs.renameSync(path.join(root,file),path.join(root,'.pending-'+randomUUID()));return root;}
test('full startup queue drains then recovers unpublished receipts without a new turn',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-capacity-')),scheduler=clock();let guard;
 try{
  const store=createTaskReceiptStore({...config,directory,limit:1});store.put({generation:7,session_id:'s',task_id:'old',cancelled:true});unpublish(directory);store.put({generation:7,session_id:'s',task_id:'published',cancelled:false});
  const calls=[];guard=createRuntimeTaskGuard({...config,receiptDirectory:directory,maxPendingReceipts:1,scheduler,fetcher:async(url,options)=>{assert.ok(url.endsWith('/finish'));calls.push(JSON.parse(options.body));return Response.json({accepted:true});}});
  assert.equal(guard.receiptStatus().recoveryPending,true);
  for(let i=0;i<10&&scheduler.timers.size;i++)await scheduler.tick();
  assert.equal(scheduler.timers.size,0);assert.equal(guard.pendingReceipts,0);assert.equal(guard.receiptStatus().recoveryPending,false);
  assert.deepEqual(calls.map(r=>r.task_id).sort(),['old','published']);assert.equal(calls.find(r=>r.task_id==='old').cancelled,true);assert.deepEqual(store.list(),[]);
 }finally{guard?.stopReceiptRetries();fs.rmSync(directory,{recursive:true,force:true});}
});
test('bounded directory scans progress past corrupt and unknown entries then stop',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-scan-')),scheduler=clock();let guard;
 try{
  const store=createTaskReceiptStore({...config,directory});const root=path.join(directory,fs.readdirSync(directory)[0]);
  for(let i=0;i<300;i++)fs.writeFileSync(path.join(root,i%2?'.pending-'+randomUUID():'unknown-'+i),'partial');
  store.put({generation:7,session_id:'s',task_id:'last',cancelled:true});unpublish(directory);
  let calls=0;guard=createRuntimeTaskGuard({...config,receiptDirectory:directory,scheduler,fetcher:async(url,options)=>{assert.ok(url.endsWith('/finish'));assert.equal(JSON.parse(options.body).task_id,'last');calls++;return Response.json({accepted:true});}});
  assert.equal(guard.receiptStatus().recoveryPending,true);
  for(let i=0;i<10&&scheduler.timers.size;i++)await scheduler.tick();
  assert.equal(calls,1);assert.equal(scheduler.timers.size,0);assert.equal(guard.receiptStatus().recoveryPending,false);assert.equal(fs.readdirSync(root).length,300);
 }finally{guard?.stopReceiptRetries();fs.rmSync(directory,{recursive:true,force:true});}
});
test('pending scan blocks new dispatch and stopping retries preserves resumable files',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-stop-scan-')),scheduler=clock();let guard;
 try{
  createTaskReceiptStore({...config,directory});const root=path.join(directory,fs.readdirSync(directory)[0]);for(let i=0;i<500;i++)fs.writeFileSync(path.join(root,'unknown-'+i),'partial');
  guard=createRuntimeTaskGuard({...config,receiptDirectory:directory,scheduler,fetcher:async()=>{throw new Error('no network expected');}});
  await assert.rejects(guard.run({sessionId:'s',taskId:'new'},async()=>{assert.fail('must not dispatch');}),e=>e.code==='TASK_RECEIPT_PENDING');
  guard.stopReceiptRetries();assert.equal(scheduler.timers.size,0);assert.equal(fs.readdirSync(root).length,500);
  for(let i=0;i<10&&guard.receiptStatus().recoveryPending;i++)await guard.flushReceipts();
  assert.equal(guard.receiptStatus().recoveryPending,false);assert.equal(scheduler.timers.size,0);
 }finally{guard?.stopReceiptRetries();fs.rmSync(directory,{recursive:true,force:true});}
});
test('failed receipt retries without another task, backs off, and stops when empty',async()=>{
 const scheduler=clock(),statuses=[];let failures=2,finishCalls=0;
 const guard=createRuntimeTaskGuard({...config,scheduler,receiptRetryMs:10,receiptRetryMaxMs:20,onReceiptStatus:s=>statuses.push(s),fetcher:async url=>{
  if(url.endsWith('/check'))return Response.json({revoked:false,finished:false});finishCalls++;return failures-->0?new Response('private upstream detail',{status:503}):Response.json({accepted:true});
 }});
 await guard.run({sessionId:'s',taskId:'t'},async()=>{});
 assert.equal(finishCalls,1);assert.equal(scheduler.timers.values().next().value.delay,10);
 await scheduler.tick();assert.equal(finishCalls,2);assert.equal(scheduler.timers.values().next().value.delay,20);
 await scheduler.tick();assert.equal(finishCalls,3);assert.equal(guard.pendingReceipts,0);assert.equal(scheduler.timers.size,0);
 assert.equal(guard.receiptStatus().lastErrorCode,null);assert.ok(!JSON.stringify(statuses).includes(config.apiKey));assert.ok(!JSON.stringify(statuses).includes('private upstream detail'));
});
test('restored queue is scheduled immediately without model dispatch',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-retry-'));const scheduler=clock();
 try{
  createTaskReceiptStore({...config,directory}).put({generation:7,session_id:'s',task_id:'t',cancelled:true});
  let calls=0;const guard=createRuntimeTaskGuard({...config,receiptDirectory:directory,scheduler,fetcher:async(url,options)=>{calls++;assert.ok(url.endsWith('/finish'));assert.equal(JSON.parse(options.body).cancelled,true);return Response.json({accepted:true});}});
  assert.equal(scheduler.timers.size,1);await scheduler.tick();assert.equal(calls,1);assert.equal(guard.pendingReceipts,0);
 }finally{fs.rmSync(directory,{recursive:true,force:true});}
});
test('retry shutdown retains receipts and diagnostic callbacks cannot break execution',async()=>{
 const scheduler=clock();const guard=createRuntimeTaskGuard({...config,scheduler,onReceiptStatus:()=>{throw new Error('observer');},fetcher:async url=>url.endsWith('/check')?Response.json({revoked:false,finished:false}):new Response('',{status:503})});
 await guard.run({sessionId:'s',taskId:'t'},async()=>{});guard.stopReceiptRetries();assert.equal(scheduler.timers.size,0);assert.equal(guard.pendingReceipts,1);assert.equal(guard.receiptStatus().retriesStopped,true);
 await guard.flushReceipts();assert.equal(scheduler.timers.size,0);
});
test('no receipt means no background polling and invalid delay is rejected',()=>{
 const scheduler=clock();createRuntimeTaskGuard({...config,scheduler});assert.equal(scheduler.timers.size,0);
 assert.throws(()=>createRuntimeTaskGuard({...config,receiptRetryMs:0}),e=>e.code==='INVALID_TASK_IDENTITY');
});
test('native bridge restores and retries on module load without a user turn',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-bridge-retry-')),scheduler=clock();
 const env={...process.env},fetcher=globalThis.fetch,setTimer=globalThis.setTimeout,clearTimer=globalThis.clearTimeout;
 try{
  createTaskReceiptStore({...config,directory}).put({generation:7,session_id:'s',task_id:'t',cancelled:true});
  Object.assign(process.env,{CLAWMANAGER_OPENCLAW_TASK_PROTOCOL:'1',CLAWMANAGER_TASK_RECEIPT_DIR:directory,CLAWMANAGER_LLM_BASE_URL:config.baseURL,CLAWMANAGER_LLM_API_KEY:config.apiKey,CLAWMANAGER_GATEWAY_GENERATION:'7'});
  globalThis.setTimeout=scheduler.setTimeout;globalThis.clearTimeout=scheduler.clearTimeout;
  let calls=0;globalThis.fetch=async(url,options)=>{calls++;assert.ok(url.endsWith('/finish'));assert.equal(JSON.parse(options.body).task_id,'t');return Response.json({accepted:true});};
  const bridge=await import('./managed_turn_bridge.mjs?startup-retry-test');
  assert.equal(bridge.managedTaskReceiptStatus().pending,1);assert.equal(scheduler.timers.size,1);
  await scheduler.tick();assert.equal(calls,1);assert.equal(bridge.managedTaskReceiptStatus().pending,0);assert.equal(scheduler.timers.size,0);
 }finally{process.env=env;globalThis.fetch=fetcher;globalThis.setTimeout=setTimer;globalThis.clearTimeout=clearTimer;fs.rmSync(directory,{recursive:true,force:true});}
});
test('disabled native bridge does not initialize a receipt directory or timer',async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-bridge-disabled-')),scheduler=clock();
 const env={...process.env},setTimer=globalThis.setTimeout,clearTimer=globalThis.clearTimeout;
 try{
  const target=path.join(directory,'must-not-exist');
  Object.assign(process.env,{CLAWMANAGER_OPENCLAW_TASK_PROTOCOL:'0',CLAWMANAGER_TASK_RECEIPT_DIR:target,CLAWMANAGER_LLM_BASE_URL:config.baseURL,CLAWMANAGER_LLM_API_KEY:config.apiKey,CLAWMANAGER_GATEWAY_GENERATION:'7'});
  globalThis.setTimeout=scheduler.setTimeout;globalThis.clearTimeout=scheduler.clearTimeout;
  const bridge=await import('./managed_turn_bridge.mjs?startup-disabled-test');
  assert.deepEqual(bridge.managedTaskReceiptStatus(),{initialized:false});assert.equal(scheduler.timers.size,0);assert.equal(fs.existsSync(target),false);
 }finally{process.env=env;globalThis.setTimeout=setTimer;globalThis.clearTimeout=clearTimer;fs.rmSync(directory,{recursive:true,force:true});}
});
