import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {createRuntimeTaskGuard, RuntimeTaskError, RuntimeTaskCleanupError} from './runtime_task_guard.mjs';
const deferred = () => { let resolve; const promise = new Promise(r => {resolve=r;}); return {promise,resolve}; };
const turn = {sessionId:'session-1',taskId:'user-message-1'};
const baseURL='http://gateway.invalid/v1',apiKey='test-key-not-a-real-secret';
function fixture(extra={}) {
 const calls=[], states=new Map();
 let intercept;
 const fetcher=async(url,options)=>{
  assert.equal(options.method,'POST');assert.equal(options.redirect,'error');
  assert.equal(options.headers.Authorization,'Bearer '+apiKey);
  const body=JSON.parse(options.body),path=url.replace(baseURL+'/runtime/','');
  assert.equal(body.generation,7);assert.equal(body.user_id,undefined);assert.equal(body.instance_id,undefined);
  const call={path,body,signal:options.signal};calls.push(call);
  const key=JSON.stringify([body.session_id,body.task_id]);
  const state=states.get(key)??{revoked:false,finished:false};states.set(key,state);
  const replaced=await intercept?.(call,state);
  if(replaced)return replaced;
  if(path==='tasks/check')return state.revoked||state.finished ? new Response('',{status:403}):Response.json(state);
  assert.equal(path,'tasks/finish');state.finished=true;return Response.json({accepted:true});
 };
 const guard=createRuntimeTaskGuard({baseURL,apiKey,generation:7,fetcher,pollMs:5,timeoutMs:100,...extra});
 return {guard,calls,states,set intercept(value){intercept=value;}};
}
const receipts=f=>f.calls.filter(c=>c.path==='tasks/finish');

test('normal completion propagates exact scoped headers and never acknowledges cancellation',async()=>{
 const f=fixture();
 const value=await f.guard.run(turn,async({signal,headers})=>{
  assert.equal(signal.aborted,false);assert.ok(Object.isFrozen(headers));
  assert.deepEqual(headers,{'X-ClawManager-Session-ID':'session-1','X-ClawManager-Task-ID':'user-message-1','X-ClawManager-Runtime-Generation':'7'});
  return 'result';
 });
 assert.equal(value,'result');assert.equal(receipts(f).length,1);assert.equal(receipts(f)[0].body.cancelled,false);
 assert.equal(f.guard.pendingReceipts,0);
});
test('revoke aborts operation but receipt waits for tool cleanup',{timeout:3000},async()=>{
 const f=fixture(),aborted=deferred(),cleanup=deferred();let checks=0,stopped=false;
 f.intercept=async(call,state)=>{if(call.path==='tasks/check'&&++checks>1)state.revoked=true;};
 const running=f.guard.run(turn,async({signal})=>{
  await new Promise(resolve=>signal.addEventListener('abort',()=>{aborted.resolve();resolve();},{once:true}));
  await cleanup.promise;stopped=true;return 'must not report success';
 });
 const rejected=assert.rejects(running,e=>e instanceof RuntimeTaskError&&e.code==='MODEL_UNAVAILABLE');
 await aborted.promise;assert.equal(stopped,false);assert.equal(receipts(f).length,0);
 cleanup.resolve();await rejected;
 assert.equal(stopped,true);assert.equal(receipts(f)[0].body.cancelled,true);
});
test('authorization outage stops execution without claiming revoked receipt',{timeout:3000},async()=>{
 const f=fixture();let checks=0;
 f.intercept=async call=>call.path==='tasks/check'&&++checks>1?new Response('secret diagnostic',{status:503}):undefined;
 await assert.rejects(f.guard.run(turn,({signal})=>new Promise(resolve=>signal.addEventListener('abort',resolve,{once:true}))),e=>
  e.code==='MODEL_AUTHORIZATION_UNAVAILABLE'&&!e.message.includes('secret diagnostic'));
 assert.equal(receipts(f)[0].body.cancelled,false);
});
test('caller cancellation joins cleanup without inventing policy revocation',{timeout:3000},async()=>{
 const f=fixture(),controller=new AbortController();
 await assert.rejects(f.guard.run({...turn,signal:controller.signal},async({signal})=>{
  controller.abort();assert.equal(signal.aborted,true);return 'ignored';
 }),e=>e.code==='ABORTED');
 assert.equal(receipts(f)[0].body.cancelled,false);
});
test('preflight denial dispatches no work and records no cancellation acknowledgement',async()=>{
 const f=fixture();let work=0;
 f.intercept=async(call,state)=>{if(call.path==='tasks/check')state.revoked=true;};
 await assert.rejects(f.guard.run(turn,async()=>work++),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(work,0);assert.equal(receipts(f).length,0);assert.equal(f.guard.pendingReceipts,0);
});
test('bad identity and already aborted input make no request',async()=>{
 const f=fixture(),controller=new AbortController();controller.abort();
 for(const id of ['', ' a', 'x\ny','中文','a'.repeat(192)]) {
  await assert.rejects(f.guard.run({...turn,taskId:id},async()=>{}),e=>e.code==='INVALID_TASK_IDENTITY');
 }
 await assert.rejects(f.guard.run({...turn,signal:controller.signal},async()=>{}),e=>e.code==='ABORTED');
 assert.equal(f.calls.length,0);
});
test('generation and endpoint configuration fail closed',()=>{
 for(const extra of [{generation:undefined},{generation:-1},{generation:NaN},{generation:2147483648},
  {baseURL:'file:///private'},{baseURL:'http://user:pass@host/v1'},{baseURL:'http://host/v1?secret=1'},
  {baseURL:'http://host/v1#fragment'},{apiKey:''},{apiKey:'x\ny'},{pollMs:0},{timeoutMs:0},{maxPendingReceipts:0}]) {
  assert.throws(()=>createRuntimeTaskGuard({baseURL,apiKey,generation:7,...extra}),e=>e.code==='INVALID_TASK_IDENTITY');
 }
});
test('duplicate in-flight identity cannot start or finish another execution',{timeout:3000},async()=>{
 const f=fixture(),started=deferred(),release=deferred();
 const running=f.guard.run(turn,async()=>{started.resolve();await release.promise;});
 await started.promise;
 await assert.rejects(f.guard.run(turn,async()=>{}),e=>e.code==='TASK_ALREADY_RUNNING');
 assert.equal(receipts(f).length,0);release.resolve();await running;assert.equal(receipts(f).length,1);
});
test('failed cancellation receipt is retained and retried with unchanged identity and flag',{timeout:3000},async()=>{
 const f=fixture();let checks=0,failFinish=true;
 f.intercept=async(call,state)=>{
  if(call.path==='tasks/check'&&++checks>1)state.revoked=true;
  if(call.path==='tasks/finish'&&failFinish)return new Response('',{status:503});
 };
 await assert.rejects(f.guard.run(turn,({signal})=>new Promise(resolve=>signal.addEventListener('abort',resolve,{once:true}))),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(f.guard.pendingReceipts,1);const original=receipts(f)[0].body;
 failFinish=false;await f.guard.flushReceipts();assert.equal(f.guard.pendingReceipts,0);
 assert.deepEqual(receipts(f)[1].body,original);assert.equal(original.cancelled,true);
});
test('backlog is bounded and admission failure never manufactures a receipt',async()=>{
 const f=fixture({maxPendingReceipts:1});
 f.intercept=async call=>call.path==='tasks/finish'?new Response('',{status:503}):undefined;
 await f.guard.run(turn,async()=>{});assert.equal(f.guard.pendingReceipts,1);
 await assert.rejects(f.guard.run({...turn,taskId:'other'},async()=>{throw new Error('must not start');}),e=>e.code==='TASK_RECEIPT_PENDING');
 assert.equal(f.guard.pendingReceipts,1);assert.ok(f.calls.every(c=>c.body.task_id===turn.taskId));
});
test('late denial after operation settled is not a cancellation receipt',{timeout:3000},async()=>{
 const f=fixture(),checking=deferred(),releaseCheck=deferred(),releaseOperation=deferred();let checks=0;
 f.intercept=async call=>{
  if(call.path==='tasks/check'&&++checks>1) {checking.resolve();await releaseCheck.promise;return new Response('',{status:403});}
 };
 const running=f.guard.run(turn,async()=>{await releaseOperation.promise;return 'done';});
 await checking.promise;releaseOperation.resolve();
 // Allow the operation continuation to mark settled before the check returns.
 await new Promise(resolve=>setImmediate(resolve));releaseCheck.resolve();
 assert.equal(await running,'done');assert.equal(receipts(f)[0].body.cancelled,false);
});
test('normal operation errors are preserved and cannot forge cancellation',async()=>{
 const f=fixture(),error=new RuntimeTaskError('MODEL_UNAVAILABLE',true);
 await assert.rejects(f.guard.run(turn,async()=>{throw error;}),e=>e===error);
 assert.equal(receipts(f)[0].body.cancelled,false);
});
test('malformed, oversized, or missing state never authorizes a task',async()=>{
 for(const body of ['not-json','{}','{"revoked":false,"finished":"false"}','x'.repeat(4097)]) {
  const f=fixture();let work=0;
  f.intercept=async call=>call.path==='tasks/check'?new Response(body):undefined;
  await assert.rejects(f.guard.run(turn,async()=>work++),e=>e.code==='MODEL_AUTHORIZATION_UNAVAILABLE');
  assert.equal(work,0);
 }
});
test('timeout includes a stalled body, not only response headers',{timeout:3000},async()=>{
 const f=fixture({timeoutMs:25});
 f.intercept=async call=>call.path==='tasks/check'?new Response(new ReadableStream({start(controller){
  call.signal.addEventListener('abort',()=>controller.error(new Error(apiKey)),{once:true});
 }})):undefined;
 await assert.rejects(f.guard.run(turn,async()=>{throw new Error('must not start');}),e=>e.code==='MODEL_AUTHORIZATION_UNAVAILABLE'&&!e.message.includes(apiKey));
});
test('same task string in different sessions remains isolated',{timeout:3000},async()=>{
 const f=fixture(),ready=deferred(),release=deferred();
 const first=f.guard.run(turn,async()=>{ready.resolve();await release.promise;return 1;});
 await ready.promise;
 assert.equal(await f.guard.run({...turn,sessionId:'session-2'},async()=>2),2);
 release.resolve();assert.equal(await first,1);assert.equal(receipts(f).length,2);
});
test('malformed receipt response remains unconfirmed',async()=>{
 const f=fixture();f.intercept=async call=>call.path==='tasks/finish'?Response.json({accepted:false}):undefined;
 await f.guard.run(turn,async()=>{});assert.equal(f.guard.pendingReceipts,1);
});
test('receipt flush and normal settlement share one in-flight submission',{timeout:3000},async()=>{
 const f=fixture(),sending=deferred(),release=deferred();
 f.intercept=async call=>{if(call.path==='tasks/finish'){sending.resolve();await release.promise;}};
 const running=f.guard.run(turn,async()=>{});await sending.promise;
 const flushes=Promise.all([f.guard.flushReceipts(),f.guard.flushReceipts()]);
 assert.equal(receipts(f).length,1);release.resolve();await Promise.all([running,flushes]);
 assert.equal(receipts(f).length,1);assert.equal(f.guard.pendingReceipts,0);
});
test('reusing a finished task is rejected before any new dispatch',async()=>{
 const f=fixture();let dispatches=0;
 await f.guard.run(turn,async()=>dispatches++);
 await assert.rejects(f.guard.run(turn,async()=>dispatches++),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(dispatches,1);assert.ok(receipts(f).every(call=>call.body.cancelled===false));
});
test('real HTTP stream is closed before a cancellation receipt is sent',{timeout:15000},async()=>{
 const closed=deferred(),errors=[];let bound=false,streamClosed=false,finished=false;
 const server=http.createServer(async(req,res)=>{
  try {
   assert.equal(req.headers.authorization,'Bearer '+apiKey);
   if(req.url==='/v1/chat/completions') {
    assert.equal(req.headers['x-clawmanager-session-id'],turn.sessionId);
    assert.equal(req.headers['x-clawmanager-task-id'],turn.taskId);
    assert.equal(req.headers['x-clawmanager-runtime-generation'],'7');
    bound=true;
    res.on('close',()=>{streamClosed=true;closed.resolve();});
    res.writeHead(200,{'Content-Type':'text/event-stream'});res.write('data: {"delta":"part"}\n\n');
    return;
   }
   let raw='';for await(const chunk of req)raw+=chunk;
   const body=JSON.parse(raw);assert.equal(body.session_id,turn.sessionId);assert.equal(body.task_id,turn.taskId);
   if(req.url==='/v1/runtime/tasks/check') {
    res.writeHead(bound?403:200,{'Content-Type':'application/json'});
    res.end(JSON.stringify(bound?{error:'revoked'}:{revoked:false,finished:false}));return;
   }
   assert.equal(req.url,'/v1/runtime/tasks/finish');assert.equal(body.cancelled,true);assert.equal(streamClosed,true);
   finished=true;res.writeHead(200,{'Content-Type':'application/json'});res.end('{"accepted":true}');
  } catch(error) {errors.push(error);res.writeHead(500);res.end();}
 });
 await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
 const base='http://127.0.0.1:'+server.address().port+'/v1';
 // This uses real HTTP/undici, including cold initialization. Keep a bounded
 // deadline, but allow CPU-limited joint suites to reach the revocation path.
 // Production deadlines and the stream-close-before-finish assertions stay unchanged.
 const guard=createRuntimeTaskGuard({baseURL:base,apiKey,generation:7,pollMs:10,timeoutMs:5000});
 try {
  await assert.rejects(guard.run(turn,async({signal,headers})=>{
   let reader;
   try {
    const response=await fetch(base+'/chat/completions',{method:'POST',headers:{...headers,Authorization:'Bearer '+apiKey},signal});
    reader=response.body.getReader();while(!(await reader.read()).done){}
   } finally {
    await reader?.cancel().catch(()=>{});
    if(bound)await closed.promise; // Simulates joining the native operation's cleanup.
   }
  }),e=>e.code==='MODEL_UNAVAILABLE');
  assert.deepEqual(errors,[]);assert.equal(bound,true);assert.equal(finished,true);assert.equal(guard.pendingReceipts,0);
 } finally {server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
});
test('failed cleanup after revocation never claims the tool stopped',{timeout:3000},async()=>{
 const f=fixture();let checks=0;
 f.intercept=async(call,state)=>{if(call.path==='tasks/check'&&++checks>1)state.revoked=true;};
 await assert.rejects(f.guard.run(turn,async({signal})=>{
  await new Promise(resolve=>signal.addEventListener('abort',resolve,{once:true}));
  throw new RuntimeTaskCleanupError();
 }),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(receipts(f).length,0);assert.equal(f.guard.pendingReceipts,0);
 assert.equal(f.guard.receiptStatus().cleanupUnconfirmed,1);
 assert.equal(f.guard.receiptStatus().storageBlocked,true);
 await f.guard.flushReceipts();assert.equal(receipts(f).length,0);
 await assert.rejects(f.guard.run({...turn,taskId:'next'},async()=>assert.fail('must not start')),e=>e.code==='TASK_RECEIPT_PENDING');
});

test('failed preflight never sends or queues a receipt for an unregistered task',async()=>{
 for(const status of [403,503]) {
  const f=fixture();f.intercept=async call=>call.path==='tasks/check'?new Response('',{status}):undefined;
  await assert.rejects(f.guard.run(turn,async()=>assert.fail('must not start')));
  await f.guard.flushReceipts();assert.equal(receipts(f).length,0);assert.equal(f.guard.pendingReceipts,0);
 }
});

test('cleanup failure without revocation is not a normal completion',async()=>{
 const f=fixture();
 await assert.rejects(f.guard.run(turn,async()=>{throw new RuntimeTaskCleanupError();}));
 assert.equal(receipts(f).length,0);assert.equal(f.guard.receiptStatus().cleanupUnconfirmed,1);
 await assert.rejects(f.guard.run(turn,async()=>assert.fail('must not redispatch')),e=>e.code==='TASK_RECEIPT_PENDING');
});
