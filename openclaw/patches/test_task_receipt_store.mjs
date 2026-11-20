import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {createTaskReceiptStore} from './task_receipt_store.mjs';
import {createRuntimeTaskGuard,RuntimeTaskCleanupError} from './runtime_task_guard.mjs';
import {spawnSync} from 'node:child_process';
let directory;
test.beforeEach(()=>{directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-receipts-'));});
test.afterEach(()=>fs.rmSync(directory,{recursive:true,force:true}));
const config=()=>({directory,baseURL:'http://gateway.invalid/v1',apiKey:'private-test-key',generation:7});
const receipt={generation:7,session_id:'s',task_id:'t',cancelled:true};
const executionRoot=()=>path.join(directory,'z-task-executions');
const guardConfig=()=>({...config(),receiptDirectory:directory,fetcher:async url=>url.endsWith('/check')?Response.json({revoked:false,finished:false}):Response.json({accepted:true})});
test('maintenance lock blocks startup, active-store mutations and new dispatch',async()=>{
 const store=createTaskReceiptStore(config()),token=store.beginExecution(receipt);store.put(receipt);
 const guard=createRuntimeTaskGuard(guardConfig());
 fs.mkdirSync(path.join(directory,'.task-maintenance.lock'));
 for(const operation of [()=>store.pendingExecutions(),()=>store.beginExecution({...receipt,task_id:'new'}),()=>store.completeExecution(token,receipt),()=>store.put(receipt),()=>store.remove(receipt),()=>store.recoverUnpublished(),()=>store.maintainTemporaryFiles()])assert.throws(operation,/maintenance/);
 assert.throws(()=>createRuntimeTaskGuard(guardConfig()),e=>e.code==='TASK_RECEIPT_PENDING');
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'new'},async()=>assert.fail('must not execute')),e=>e.code==='TASK_RECEIPT_PENDING');}finally{guard.stopReceiptRetries();}
 assert.equal(store.list().length,1);assert.equal(fs.readdirSync(executionRoot()).length,1);
});
test('maintenance appearing after guard initialization blocks an otherwise idle dispatch',async()=>{
 const guard=createRuntimeTaskGuard(guardConfig());fs.writeFileSync(path.join(directory,'.task-maintenance.lock'),'partial');
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'new'},async()=>assert.fail('must not execute')),e=>e.code==='TASK_RECEIPT_PENDING');assert.equal(guard.pendingReceipts,0);}finally{guard.stopReceiptRetries();}
});
test('maintenance appearing during execution marker sync prevents native dispatch',async()=>{
 const guard=createRuntimeTaskGuard(guardConfig()),previous=fs.fsyncSync;let locked=false,ran=false;
 fs.fsyncSync=fd=>{previous(fd);if(!locked&&fs.existsSync(executionRoot())&&fs.readdirSync(executionRoot()).length){fs.mkdirSync(path.join(directory,'.task-maintenance.lock'));locked=true;}};
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'new'},async()=>{ran=true;}),e=>e.code==='TASK_RECEIPT_PENDING');assert.equal(locked,true);assert.equal(ran,false);assert.equal(guard.pendingReceipts,0);assert.equal(fs.readdirSync(executionRoot()).length,1);}
 finally{fs.fsyncSync=previous;guard.stopReceiptRetries();}
});
test('execution barrier exists before dispatch and clears only after settled publication',async()=>{
 const guard=createRuntimeTaskGuard(guardConfig());
 try{
  await guard.run({sessionId:'s',taskId:'t'},async()=>{
   assert.equal(createTaskReceiptStore(config()).pendingExecutions(),1);
   const record=fs.readFileSync(path.join(executionRoot(),fs.readdirSync(executionRoot())[0]),'utf8');
   assert.ok(!record.includes(config().apiKey));assert.ok(!record.includes('cancelled'));
  });
  assert.equal(createTaskReceiptStore(config()).pendingExecutions(),0);
  assert.equal(createRuntimeTaskGuard(guardConfig()).receiptStatus().storageBlocked,false);
 }finally{guard.stopReceiptRetries();}
});
test('cleanup failure remains blocked on restart and does not manufacture receipts',async()=>{
 const first=createRuntimeTaskGuard(guardConfig());
 try{await assert.rejects(first.run({sessionId:'s',taskId:'t'},async()=>{throw new RuntimeTaskCleanupError();}),RuntimeTaskCleanupError);}
 finally{first.stopReceiptRetries();}
 assert.equal(createTaskReceiptStore(config()).list().length,0);
 for(const extra of [{},{generation:8},{apiKey:'rotated'},{baseURL:'http://new.invalid/v1'}]){
  let network=0,dispatch=0;const guard=createRuntimeTaskGuard({...guardConfig(),...extra,fetcher:async()=>{network++;throw new Error('unexpected network');}});
  try{
   assert.equal(guard.receiptStatus().recoveredExecutions,1);
   assert.equal(guard.receiptStatus().lastErrorCode,'TASK_EXECUTION_RECOVERY_REQUIRED');
   await assert.rejects(guard.run({sessionId:'s',taskId:'next'},async()=>{dispatch++;}),e=>e.code==='TASK_RECEIPT_PENDING');
   await guard.flushReceipts();assert.equal(network,0);assert.equal(dispatch,0);
   assert.equal(guard.receiptStatus().nextRetryAt,null);
  }finally{guard.stopReceiptRetries();}
 }
});
test('a real child process exit during dispatch leaves a blocking journal',()=>{
 const moduleURL=new URL('./runtime_task_guard.mjs',import.meta.url).href;
 const source=`import {createRuntimeTaskGuard} from ${JSON.stringify(moduleURL)}; const guard=createRuntimeTaskGuard({...${JSON.stringify({...config(),receiptDirectory:directory})},fetcher:async()=>Response.json({revoked:false,finished:false})}); await guard.run({sessionId:'s',taskId:'crash'},async()=>process.exit(23));`;
 // Supply an in-memory successful preflight. No server, model or tools are contacted.
 const child=spawnSync(process.execPath,['--input-type=module','-e',source],{encoding:'utf8',timeout:10000});
 assert.equal(child.status,23,child.stderr);
 const guard=createRuntimeTaskGuard(guardConfig());
 try{assert.equal(guard.receiptStatus().recoveredExecutions,1);assert.equal(guard.pendingReceipts,0);}finally{guard.stopReceiptRetries();}
});
test('unknown and partial execution entries block without being read or removed',async()=>{
 fs.mkdirSync(executionRoot());fs.writeFileSync(path.join(executionRoot(),'partial'),'broken');fs.mkdirSync(path.join(executionRoot(),'unknown'));
 const guard=createRuntimeTaskGuard(guardConfig());
 try{assert.equal(guard.receiptStatus().recoveredExecutions,2);await assert.rejects(guard.run({sessionId:'s',taskId:'t'},async()=>{}),e=>e.code==='TASK_RECEIPT_PENDING');await guard.flushReceipts();assert.equal(fs.readdirSync(executionRoot()).length,2);}finally{guard.stopReceiptRetries();}
});
test('journal filesystem failure prevents dispatch and finish',async()=>{
 const guard=createRuntimeTaskGuard(guardConfig());fs.writeFileSync(executionRoot(),'not a directory');let ran=false;
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'t'},async()=>{ran=true;}),e=>e.code==='TASK_RECEIPT_PENDING');assert.equal(ran,false);assert.equal(guard.pendingReceipts,0);assert.equal(guard.receiptStatus().storageBlocked,true);}finally{guard.stopReceiptRetries();}
});
test('only the creating process may remove a barrier with its matching published receipt',()=>{
 const store=createTaskReceiptStore(config()),token=store.beginExecution(receipt);
 assert.throws(()=>store.completeExecution(token,receipt));assert.equal(store.pendingExecutions(),1);
 store.put(receipt);
 assert.throws(()=>createTaskReceiptStore(config()).completeExecution(token,receipt),/not owned/);
 assert.throws(()=>store.completeExecution(token,{...receipt,task_id:'other'}),/not owned/);
 store.completeExecution(token,receipt);assert.equal(store.pendingExecutions(),0);
});
test('published receipt beside a leftover journal is not automatically replayed',async()=>{
 const store=createTaskReceiptStore(config());store.beginExecution(receipt);store.put(receipt);let calls=0;
 const guard=createRuntimeTaskGuard({...guardConfig(),fetcher:async()=>{calls++;throw new Error('must not send');}});
 try{await guard.flushReceipts();assert.equal(calls,0);assert.equal(guard.pendingReceipts,1);assert.equal(store.pendingExecutions(),1);assert.equal(guard.receiptStatus().nextRetryAt,null);}finally{guard.stopReceiptRetries();}
});
test('denied preflight creates no execution journal and oversized journal fails closed',async()=>{
 const guard=createRuntimeTaskGuard({...guardConfig(),fetcher:async()=>new Response('',{status:403})});
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'t'},async()=>{}));assert.equal(fs.existsSync(executionRoot()),false);}finally{guard.stopReceiptRetries();}
 fs.mkdirSync(executionRoot());fs.writeFileSync(path.join(executionRoot(),'one'),'');fs.writeFileSync(path.join(executionRoot(),'two'),'');
 assert.throws(()=>createRuntimeTaskGuard({...guardConfig(),maxPendingReceipts:1}),e=>e.code==='TASK_RECEIPT_PENDING');
});
test('journal fsync failure prevents dispatch and leaves uncertainty across restart',async()=>{
 const guard=createRuntimeTaskGuard(guardConfig()),previous=fs.fsyncSync;let ran=false;
 fs.fsyncSync=()=>{throw Object.assign(new Error('injected sync failure'),{code:'EIO'});};
 try{await assert.rejects(guard.run({sessionId:'s',taskId:'t'},async()=>{ran=true;}),e=>e.code==='TASK_RECEIPT_PENDING');assert.equal(ran,false);assert.equal(guard.pendingReceipts,0);}
 finally{fs.fsyncSync=previous;guard.stopReceiptRetries();}
 // Linux may fail syncing the parent before any file exists: dispatch never
 // occurred. Windows reaches the file fsync and leaves a conservative barrier.
 if(createTaskReceiptStore(config()).pendingExecutions()){
  const restarted=createRuntimeTaskGuard(guardConfig());try{assert.equal(restarted.receiptStatus().storageBlocked,true);}finally{restarted.stopReceiptRetries();}
 }
});
test('journal removal failure prevents finish; same-process retry preserves settled result',async()=>{
 let finishes=0;const settings=guardConfig(),fetcher=settings.fetcher;
 const guard=createRuntimeTaskGuard({...settings,fetcher:async(...args)=>{if(args[0].endsWith('/finish'))finishes++;return fetcher(...args);}});
 const previous=fs.unlinkSync;
 fs.unlinkSync=file=>{if(path.dirname(file)===executionRoot())throw Object.assign(new Error('injected unlink failure'),{code:'EACCES'});return previous(file);};
 try{await guard.run({sessionId:'s',taskId:'t'},async()=>{});assert.equal(finishes,0);assert.equal(guard.pendingReceipts,1);assert.equal(guard.receiptStatus().storageBlocked,true);}
 finally{fs.unlinkSync=previous;}
 try{await guard.flushReceipts();assert.equal(finishes,1);assert.equal(guard.pendingReceipts,0);assert.equal(createTaskReceiptStore(config()).pendingExecutions(),0);assert.equal(guard.receiptStatus().storageBlocked,false);}finally{guard.stopReceiptRetries();}
});
const file=()=>path.join(directory,fs.readdirSync(directory)[0],fs.readdirSync(path.join(directory,fs.readdirSync(directory)[0])).find(n=>n.endsWith('.json')));
test('settled receipt survives recreation and contains no credential',()=>{
 createTaskReceiptStore(config()).put(receipt);
 assert.deepEqual(createTaskReceiptStore(config()).list(),[receipt]);
 assert.ok(!fs.readFileSync(file(),'utf8').includes(config().apiKey));
 const store=createTaskReceiptStore(config());store.put(receipt);store.remove(receipt);assert.deepEqual(store.list(),[]);
});
test('different credential, endpoint and generation never adopt prior receipts',()=>{
 createTaskReceiptStore(config()).put(receipt);
 for(const extra of [{apiKey:'rotated'},{baseURL:'http://other.invalid/v1'},{generation:8}])assert.deepEqual(createTaskReceiptStore({...config(),...extra}).list(),[]);
 assert.deepEqual(createTaskReceiptStore(config()).list(),[receipt]);
});
test('edited cancellation flag fails authentication',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const target=file();
 const value=JSON.parse(fs.readFileSync(target,'utf8'));value.receipt.cancelled=false;fs.writeFileSync(target,JSON.stringify(value));
 assert.throws(()=>store.list(),/authentication/);
});
test('conflicting receipt never overwrites original confirmation',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);
 assert.throws(()=>store.put({...receipt,cancelled:false}),/Conflicting/);assert.deepEqual(store.list(),[receipt]);
});
test('queue limit and malformed files fail closed',()=>{
 const store=createTaskReceiptStore({...config(),limit:1});store.put(receipt);
 assert.throws(()=>store.put({...receipt,task_id:'other'}),/full/);
 fs.writeFileSync(file(),'x'.repeat(2049));assert.throws(()=>store.list(),/Unsafe/);
});
test('partial unpublished temporary file is not a cancellation receipt',()=>{
 const store=createTaskReceiptStore(config());const root=path.join(directory,fs.readdirSync(directory)[0]);
 fs.writeFileSync(path.join(root,'.pending-interrupted'),'partial');assert.deepEqual(store.list(),[]);
});
test('guard persists before finish and restores exact receipt on restart',async()=>{
 let fail=true;const calls=[];
 const fetcher=async(url,options)=>{
  const body=JSON.parse(options.body);calls.push({url,body});
  if(url.endsWith('/check'))return Response.json({revoked:false,finished:false});
  assert.equal(createTaskReceiptStore(config()).list().length,1);
  return fail?new Response('',{status:503}):Response.json({accepted:true});
 };
 const settings={baseURL:config().baseURL,apiKey:config().apiKey,generation:7,receiptDirectory:directory,fetcher};
 const first=createRuntimeTaskGuard(settings);await first.run({sessionId:'s',taskId:'t'},async()=>{});
 assert.equal(first.pendingReceipts,1);const second=createRuntimeTaskGuard(settings);assert.equal(second.pendingReceipts,1);
 fail=false;await second.flushReceipts();assert.equal(second.pendingReceipts,0);assert.deepEqual(createTaskReceiptStore(config()).list(),[]);
 const finishes=calls.filter(c=>c.url.endsWith('/finish'));assert.deepEqual(finishes[0].body,finishes[1].body);assert.equal(finishes[1].body.cancelled,false);
});
test('corrupt disk state stops guard initialization without network traffic',()=>{
 createTaskReceiptStore(config()).put(receipt);fs.writeFileSync(file(),'broken');
 assert.throws(()=>createRuntimeTaskGuard({baseURL:config().baseURL,apiKey:config().apiKey,generation:7,receiptDirectory:directory,fetcher:()=>{throw new Error('network');}}),e=>e.code==='TASK_RECEIPT_PENDING');
});
test('disk write failure retains memory receipt and blocks next execution',async()=>{
 const guard=createRuntimeTaskGuard({baseURL:config().baseURL,apiKey:config().apiKey,generation:7,receiptDirectory:directory,fetcher:async()=>Response.json({revoked:false,finished:false})});
 const root=path.join(directory,fs.readdirSync(directory)[0]);fs.rmdirSync(root);fs.writeFileSync(root,'not a directory');
 await guard.run({sessionId:'s',taskId:'t'},async()=>{});assert.equal(guard.pendingReceipts,1);
 await assert.rejects(guard.run({sessionId:'s',taskId:'next'},async()=>{throw new Error('must not execute');}),e=>e.code==='TASK_RECEIPT_PENDING');
});

const oldTime=()=>new Date(Date.now()-2*86400000);
function duplicate(){const target=file(),temp=path.join(path.dirname(target),'.pending-'+randomUUID());fs.linkSync(target,temp);fs.utimesSync(temp,oldTime(),oldTime());return temp;}
test('maintenance removes only old authenticated published hard-link duplicates',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const temp=duplicate();
 const result=store.maintainTemporaryFiles();assert.equal(result.removed,1);assert.equal(fs.existsSync(temp),false);assert.deepEqual(store.list(),[receipt]);
});
test('unpublished, partial, copied and recent files are retained',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const root=path.dirname(file());
 const copied=path.join(root,'.pending-'+randomUUID());fs.copyFileSync(file(),copied);fs.utimesSync(copied,oldTime(),oldTime());
 const partial=path.join(root,'.pending-'+randomUUID());fs.writeFileSync(partial,'partial');fs.utimesSync(partial,oldTime(),oldTime());
 const recent=path.join(root,'.pending-'+randomUUID());fs.linkSync(file(),recent);
 const result=store.maintainTemporaryFiles();assert.equal(result.removed,0);assert.equal(result.retained,3);assert.ok([copied,partial,recent].every(p=>fs.existsSync(p)));
});
test('tampered published copy and non-file entries cannot authorize deletion',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const temp=duplicate();fs.writeFileSync(file(),'corrupt');
 const folder=path.join(path.dirname(temp),'.pending-'+randomUUID());fs.mkdirSync(folder);
 const result=store.maintainTemporaryFiles();assert.equal(result.removed,0);assert.ok(result.errors>0);assert.ok(fs.existsSync(temp));assert.ok(fs.existsSync(folder));assert.throws(()=>store.list());
});
test('maintenance is bounded, namespace isolated, and refuses unsafe options',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const first=duplicate(),second=duplicate();
 const other=createTaskReceiptStore({...config(),generation:8});assert.equal(other.maintainTemporaryFiles().scanned,0);
 const result=store.maintainTemporaryFiles({maxEntries:1});assert.equal(result.scanned,1);assert.equal(result.removed,1);assert.equal(result.truncated,true);assert.equal([first,second].filter(p=>fs.existsSync(p)).length,1);
 assert.throws(()=>store.maintainTemporaryFiles({minAgeMs:0}));assert.throws(()=>store.maintainTemporaryFiles({maxEntries:1025}));assert.throws(()=>createTaskReceiptStore({...config(),limit:0}));
});
test('guard exposes startup maintenance counts without disclosing paths or identities',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);duplicate();
 const guard=createRuntimeTaskGuard({baseURL:config().baseURL,apiKey:config().apiKey,generation:7,receiptDirectory:directory});
 try{assert.equal(guard.receiptStatus().unpublishedRecovery.recovered,1);assert.equal(guard.pendingReceipts,1);assert.ok(!JSON.stringify(guard.receiptStatus()).includes(directory));}finally{guard.stopReceiptRetries();}
});
test('complete unpublished receipt is recovered with original identity and cancellation result',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const target=file(),temp=path.join(path.dirname(target),'.pending-'+randomUUID());fs.renameSync(target,temp);
 const result=store.recoverUnpublished();assert.equal(result.recovered,1);assert.equal(fs.existsSync(temp),false);assert.deepEqual(store.list(),[receipt]);assert.equal(store.recoverUnpublished().recovered,0);
});
test('partial, foreign and conflicting unpublished receipts are retained without overwriting',()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const target=file(),temp=path.join(path.dirname(target),'.pending-'+randomUUID());fs.renameSync(target,temp);store.put({...receipt,cancelled:false});
 const partial=path.join(path.dirname(target),'.pending-'+randomUUID());fs.writeFileSync(partial,'partial');
 const other=createTaskReceiptStore({...config(),generation:8});other.put({...receipt,generation:8});const otherRoot=fs.readdirSync(directory).map(n=>path.join(directory,n)).find(p=>p!==path.dirname(target));
 const foreign=path.join(path.dirname(target),'.pending-'+randomUUID());fs.copyFileSync(path.join(otherRoot,fs.readdirSync(otherRoot)[0]),foreign);
 const result=store.recoverUnpublished();assert.equal(result.recovered,0);assert.equal(result.retained,3);assert.deepEqual(store.list(),[{...receipt,cancelled:false}]);assert.ok([temp,partial,foreign].every(p=>fs.existsSync(p)));
});
test('startup recovery queues a complete unpublished receipt without dispatching a task',async()=>{
 const store=createTaskReceiptStore(config());store.put(receipt);const target=file();fs.renameSync(target,path.join(path.dirname(target),'.pending-'+randomUUID()));
 const calls=[];const guard=createRuntimeTaskGuard({baseURL:config().baseURL,apiKey:config().apiKey,generation:7,receiptDirectory:directory,fetcher:async(url,options)=>{calls.push({url,body:JSON.parse(options.body)});return Response.json({accepted:true});}});
 try{assert.equal(guard.pendingReceipts,1);assert.equal(guard.receiptStatus().unpublishedRecovery.recovered,1);await guard.flushReceipts();assert.equal(calls.length,1);assert.ok(calls[0].url.endsWith('/finish'));assert.deepEqual(calls[0].body,receipt);assert.equal(guard.pendingReceipts,0);}finally{guard.stopReceiptRetries();}
});
test('recovery respects queue capacity and scan bounds without losing unpublished receipts',()=>{
 const store=createTaskReceiptStore({...config(),limit:1});store.put(receipt);const target=file(),temp=path.join(path.dirname(target),'.pending-'+randomUUID());fs.renameSync(target,temp);
 store.put({...receipt,task_id:'other'});const full=store.recoverUnpublished();assert.equal(full.recovered,0);assert.equal(full.retained,1);assert.ok(fs.existsSync(temp));
 store.remove({...receipt,task_id:'other'});assert.equal(store.recoverUnpublished().recovered,1);assert.deepEqual(store.list(),[receipt]);
 const root=path.dirname(target);for(let i=0;i<3;i++)fs.writeFileSync(path.join(root,'.pending-'+randomUUID()),'partial');
 const bounded=store.recoverUnpublished({maxEntries:1});assert.ok(bounded.scanned<=1);assert.equal(bounded.truncated,true);assert.equal(bounded.recovered,0);store.closeRecovery();
 assert.throws(()=>store.recoverUnpublished({maxEntries:0}));assert.throws(()=>store.recoverUnpublished({maxEntries:1025}));
});
