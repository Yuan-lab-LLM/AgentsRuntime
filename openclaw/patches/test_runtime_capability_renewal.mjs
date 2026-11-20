import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {createCapabilityRenewal,capabilityRenewalReleaseAccepted} from './runtime_capability_renewal.mjs';
const capabilities=['task_identity','turn_cancel','cancel_receipt','live_catalog'];
const report=()=>({protocol:1,generation:7,runtime_version:'2026.10.2',adapter_version:'cm-openclaw-native-1',capabilities:[...capabilities]});
const inspect=()=>({protocol:1,generation:7,registration_is_sticky:true,required_capabilities:[...capabilities],enrolled:true,enrollment_generation:7,renewal_supported:true,report_ttl_seconds:30});
const renewed=()=>({protocol:1,generation:7,renewed:true,task_identity_required:true,report_ttl_seconds:30});
const deferred=()=>{let resolve;const promise=new Promise(r=>{resolve=r;});return {resolve,promise};};
const initial=()=>({...inspect(),enrolled:false,enrollment_generation:null,conditional_enrollment_supported:true,enrollment_revision:null});
const acknowledged=()=>({...renewed(),enrolled:true});
test('explicit enrollment proceeds GET then conditional POST then renewal',async()=>{
 for(const prior of [initial(),{...initial(),enrolled:true,enrollment_generation:6,enrollment_revision:'a'.repeat(64)}]) {
  const calls=[];const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{
   calls.push({url,...o});return Response.json(o.method==='GET'?prior:url.endsWith('/enroll')?acknowledged():renewed());
  }});
  await f.client.start();assert.equal(f.client.status().state,'active');assert.equal(calls.length,3);
  assert.ok(calls[1].url.endsWith('/capabilities/enroll'));assert.deepEqual(JSON.parse(calls[1].body),{report:report(),expected_enrollment_revision:prior.enrollment_revision});
  assert.ok(calls[2].url.endsWith('/capabilities/renew'));await f.tick();assert.equal(calls.length,4);assert.ok(calls[3].url.endsWith('/renew'));f.client.stop();
 }
});
test('lost enrollment acknowledgment retries the original precondition without re-inspecting',async()=>{
 let gets=0,posts=0;const bodies=[];
 const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{
  if(o.method==='GET'){gets++;return Response.json(initial());}
  if(url.endsWith('/enroll')){bodies.push(o.body);if(++posts===1)throw new Error('lost ack');return Response.json(acknowledged());}
  return Response.json(renewed());
 }});
 await f.client.start();assert.equal(f.client.status().state,'retrying');await f.tick();assert.equal(gets,1);assert.equal(posts,2);assert.equal(bodies[0],bodies[1]);assert.equal(f.client.status().state,'active');f.client.stop();
});
test('storage admission must be explicitly ready before enrollment',async()=>{
 let ready=false,posts=0;
 const f=fixture({allowEnrollment:true,canEnroll:()=>ready,fetcher:async(url,o)=>{
  if(o.method==='GET')return Response.json(initial());posts++;return Response.json(url.endsWith('/enroll')?acknowledged():renewed());
 }});
 await f.client.start();assert.equal(posts,0);assert.equal(f.client.status().lastErrorCode,'ENROLLMENT_NOT_READY');
 ready=true;await f.tick();assert.equal(posts,2);assert.equal(f.client.status().state,'active');f.client.stop();
});
test('missing or throwing storage readiness never enrolls',async()=>{
 for(const options of [{},{canEnroll:()=>{throw new Error('private');}}]) {
  const f=fixture({allowEnrollment:true,...options,fetcher:async(_url,o)=>{assert.equal(o.method,'GET');return Response.json(initial());}});
  await f.client.start();assert.equal(f.client.status().lastErrorCode,'ENROLLMENT_NOT_READY');f.client.stop();
 }
});
test('old or malformed conditional contracts never dispatch enrollment',async()=>{
 for(const changes of [{conditional_enrollment_supported:false},{conditional_enrollment_supported:undefined},{enrollment_revision:undefined},{enrollment_revision:'x'},
  {enrolled:true,enrollment_generation:8,enrollment_revision:'a'.repeat(64)},{enrolled:true,enrollment_generation:6,enrollment_revision:null}]) {
  const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(_url,o)=>{assert.equal(o.method,'GET');return Response.json({...initial(),...changes});}});
  await f.client.start();assert.equal(f.client.status().state,'stopped');assert.equal(f.timers.size,0);
 }
});
test('conditional conflict or denied registration never falls back to legacy POST',async()=>{
 for(const status of [400,401,403,404,405,409]) {
  const calls=[];const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{calls.push(url);return o.method==='GET'?Response.json(initial()):new Response('',{status});}});
  await f.client.start();assert.equal(f.client.status().state,'stopped');assert.equal(calls.length,2);assert.ok(calls[1].endsWith('/enroll'));assert.equal(f.timers.size,0);
 }
});
test('invalid enrollment acknowledgment stops before renewal',async()=>{
 for(const changes of [{enrolled:false},{generation:8},{task_identity_required:false},{report_ttl_seconds:0}]) {
  const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{
   if(o.method==='GET')return Response.json(initial());assert.ok(url.endsWith('/enroll'));return Response.json({...acknowledged(),...changes});
  }});
  await f.client.start();assert.equal(f.client.status().lastErrorCode,'INVALID_SERVER_CONTRACT');assert.equal(f.timers.size,0);
 }
});
test('successful registration is not repeated when the first renewal fails transiently',async()=>{
 let enrolls=0,renewals=0;
 const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{
  if(o.method==='GET')return Response.json(initial());
  if(url.endsWith('/enroll')){enrolls++;return Response.json(acknowledged());}
  return ++renewals===1?new Response('',{status:503}):Response.json(renewed());
 }});
 await f.client.start();assert.equal(f.client.status().state,'retrying');await f.tick();assert.equal(enrolls,1);assert.equal(renewals,2);assert.equal(f.client.status().state,'active');f.client.stop();
});
test('same generation only renews; enrollment option cannot replace conflicting contract',async()=>{
 const f=fixture({allowEnrollment:true,canEnroll:()=>true,fetcher:async(url,o)=>{
  if(o.method==='GET')return Response.json(inspect());assert.ok(url.endsWith('/renew'));return new Response('',{status:409});
 }});
 await f.client.start();assert.equal(f.client.status().lastErrorCode,'CONTRACT_CHANGED');assert.equal(f.timers.size,0);
});
test('enrollment opt-in cannot override release acceptance',async()=>{
 const f=fixture({releaseAccepted:false,allowEnrollment:true,canEnroll:()=>true,fetcher:()=>assert.fail('network')});
 await f.client.start();assert.equal(f.client.status().state,'release_blocked');assert.equal(f.timers.size,0);
});
function fixture(overrides={}) {
 const timers=new Map(),calls=[];let id=0;
 const scheduler={setTimeout(fn,ms){const token={id:++id,unref(){}};timers.set(token,{fn,ms});return token;},clearTimeout(token){timers.delete(token);}};
 const client=createCapabilityRenewal({enabled:true,releaseAccepted:true,baseURL:'http://gateway.invalid/api/gateway/llm/',apiKey:'private-test-key',report:report(),scheduler,
  fetcher:async(url,options)=>{calls.push({url,...options});return Response.json(options.method==='GET'?inspect():renewed());},...overrides});
 return {client,timers,calls,async tick(){assert.equal(timers.size,1);const [token,timer]=[...timers][0];timers.delete(token);await timer.fn();},delay(){return [...timers.values()][0]?.ms;}};
}
test('default and release-blocked clients do no I/O even without configuration',async()=>{
 assert.equal(capabilityRenewalReleaseAccepted,false);
 for(const options of [{},{enabled:true},{enabled:false,releaseAccepted:true}]) {
  const client=createCapabilityRenewal({...options,fetcher(){assert.fail('unexpected network');}});
  await client.start();assert.equal(client.status().attempts,0);assert.equal(client.status().state,options.enabled?'release_blocked':'disabled');
 }
});
test('preflight then renewable-only report, bounded cadence and immutable wire snapshot',async()=>{
 const payload=report();const f=fixture({report:payload});payload.generation=99;payload.capabilities.length=0;payload.apiKey='do not send';
 await f.client.start();assert.equal(f.client.status().state,'active');assert.equal(f.delay(),10000);
 assert.deepEqual(f.calls.map(c=>[c.method,new URL(c.url).pathname]),[['GET','/api/gateway/llm/runtime/capabilities'],['POST','/api/gateway/llm/runtime/capabilities/renew']]);
 assert.deepEqual(JSON.parse(f.calls[1].body),report());assert.equal(f.calls[0].body,undefined);
 assert.ok(f.calls.every(c=>c.redirect==='error'&&c.headers.Authorization==='Bearer private-test-key'));
 await f.client.start();assert.equal(f.calls.length,2);await f.tick();assert.equal(f.calls.length,3);assert.equal(f.calls[2].method,'POST');
 assert.equal(f.client.status().attempts,2);assert.ok(f.client.status().lastSuccessAt);f.client.stop();assert.equal(f.timers.size,0);
});
test('concurrent starts are single-flight',async()=>{
 const gate=deferred();let calls=0;const f=fixture({fetcher:async(_url,options)=>{calls++;if(options.method==='GET'){await gate.promise;return Response.json(inspect());}return Response.json(renewed());}});
 const a=f.client.start(),b=f.client.start();assert.equal(a,b);assert.equal(calls,1);gate.resolve();await Promise.all([a,b]);assert.equal(calls,2);assert.equal(f.timers.size,1);f.client.stop();
});
test('transient errors back off with a cap and recover without re-enrollment',async()=>{
 let posts=0;const f=fixture({retryMs:10,retryMaxMs:25,fetcher:async(_url,o)=>o.method==='GET'?Response.json(inspect()):(++posts<=4?new Response('upstream private details',{status:503}):Response.json(renewed()))});
 await f.client.start();assert.equal(f.delay(),10);await f.tick();assert.equal(f.delay(),20);await f.tick();assert.equal(f.delay(),25);await f.tick();assert.equal(f.delay(),25);
 assert.equal(f.client.status().lastErrorCode,'RENEWAL_UNAVAILABLE');assert.doesNotMatch(JSON.stringify(f.client.status()),/private/);
 await f.tick();assert.equal(f.client.status().state,'active');assert.equal(f.client.status().consecutiveFailures,0);assert.equal(f.delay(),10000);f.client.stop();
});
for(const [status,code] of [[401,'AUTHORIZATION_DENIED'],[403,'AUTHORIZATION_DENIED'],[409,'CONTRACT_CHANGED'],[404,'RENEWAL_UNSUPPORTED'],[405,'RENEWAL_UNSUPPORTED']]) {
 test(`HTTP ${status} stops renewal and cancels response body`,async()=>{
  for(const stage of ['preflight','renew']) {
  let cancelled=false;const f=fixture({fetcher:async(_url,o)=>stage==='renew'&&o.method==='GET'?Response.json(inspect()):new Response(new ReadableStream({cancel(){cancelled=true;}}),{status})});
  await f.client.start();assert.equal(f.client.status().lastErrorCode,code);assert.equal(f.client.status().state,'stopped');assert.equal(f.timers.size,0);assert.equal(cancelled,true);
  await f.client.start();assert.equal(f.client.status().attempts,1);
  }
 });
}
for(const [change,code] of [[{enrolled:false},'ENROLLMENT_REQUIRED'],[{enrollment_generation:6},'ENROLLMENT_REQUIRED'],[{generation:6},'INVALID_SERVER_CONTRACT'],[{renewal_supported:false},'RENEWAL_UNSUPPORTED'],[{required_capabilities:['task_identity']},'INVALID_SERVER_CONTRACT'],[{registration_is_sticky:false},'INVALID_SERVER_CONTRACT']]) {
 test('preflight rejects '+JSON.stringify(change),async()=>{
  let calls=0;const f=fixture({fetcher:async(_url,o)=>{calls++;assert.equal(o.method,'GET');return Response.json({...inspect(),...change});}});
  await f.client.start();assert.equal(calls,1);assert.equal(f.client.status().lastErrorCode,code);assert.equal(f.timers.size,0);
 });
}
for(const [name,body] of [['null','null'],['array','[]'],['invalid','not JSON'],['oversized',' '.repeat(8193)],['wrong TTL',JSON.stringify({...inspect(),report_ttl_seconds:0})]]) {
 test('rejects malformed response: '+name,async()=>{
  const f=fixture({fetcher:async()=>new Response(body)});await f.client.start();assert.equal(f.client.status().lastErrorCode,'INVALID_SERVER_CONTRACT');assert.equal(f.timers.size,0);
 });
}
test('renew acknowledgment must match generation and sticky task enforcement',async()=>{
 for(const change of [{generation:8},{renewed:false},{task_identity_required:false},{report_ttl_seconds:3601}]) {
  const f=fixture({fetcher:async(_url,o)=>Response.json(o.method==='GET'?inspect():{...renewed(),...change})});
  await f.client.start();assert.equal(f.client.status().lastErrorCode,'INVALID_SERVER_CONTRACT');assert.equal(f.timers.size,0);
 }
});
test('short server TTL shortens renewal interval',async()=>{
 const f=fixture({fetcher:async(_url,o)=>Response.json({...(o.method==='GET'?inspect():renewed()),report_ttl_seconds:3})});
 await f.client.start();assert.equal(f.delay(),1000);f.client.stop();
});
test('explicit stop aborts in-flight request and ignores late success',async()=>{
 const entered=deferred(),release=deferred();let signal;
 const f=fixture({fetcher:async(_url,o)=>{signal=o.signal;entered.resolve();await release.promise;return Response.json(inspect());}});
 const running=f.client.start();await entered.promise;f.client.stop();assert.equal(signal.aborted,true);release.resolve();await running;
 assert.equal(f.client.status().state,'stopped');assert.equal(f.client.status().lastSuccessAt,null);assert.equal(f.timers.size,0);
});
test('identity change before start, after preflight and between cycles stops old credentials',async()=>{
 for(const stage of ['before','response','timer']) {
  let valid=stage!=='before',calls=0;const f=fixture({isCurrent:()=>valid,fetcher:async(_url,o)=>{calls++;if(stage==='response')valid=false;return Response.json(o.method==='GET'?inspect():renewed());}});
  await f.client.start();if(stage==='timer'){valid=false;await f.tick();}
  assert.equal(f.client.status().lastErrorCode,'IDENTITY_CHANGED');assert.equal(f.timers.size,0);assert.equal(calls,{before:0,response:1,timer:2}[stage]);
 }
});
test('request timeout retries with sanitized diagnostics',async()=>{
 const keepAlive=setTimeout(()=>{},1000);
 try {
  const f=fixture({timeoutMs:10,fetcher:async(_url,{signal})=>new Promise((resolve,reject)=>{signal.addEventListener('abort',()=>reject(signal.reason),{once:true});})});
  await f.client.start();assert.equal(f.client.status().state,'retrying');assert.equal(f.client.status().lastErrorCode,'RENEWAL_UNAVAILABLE');f.client.stop();
 }finally{clearTimeout(keepAlive);}
});
test('observer exceptions do not interrupt renewal',async()=>{
 const f=fixture({onStatus(){throw new Error('observer failure');}});await f.client.start();assert.equal(f.client.status().state,'active');f.client.stop();
});
test('real HTTP body stall is bounded, not just response headers',{timeout:5000},async()=>{
 const server=http.createServer((_req,res)=>{res.writeHead(200,{'Content-Type':'application/json'});res.write('{"protocol":');});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 let f;
 try {
  f=fixture({baseURL:'http://127.0.0.1:'+server.address().port,fetcher:globalThis.fetch,timeoutMs:200});
  await f.client.start();assert.equal(f.client.status().state,'retrying');assert.equal(f.client.status().lastErrorCode,'RENEWAL_UNAVAILABLE');assert.equal(f.timers.size,1);
 }finally{f?.client.stop();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
});
test('transport exceptions never leak URLs or credentials to diagnostics',async()=>{
 const f=fixture({fetcher:async()=>{throw new Error('http://gateway.invalid/private-test-key');}});
 await f.client.start();assert.equal(f.client.status().state,'retrying');assert.doesNotMatch(JSON.stringify(f.client.status()),/gateway|private-test-key/);f.client.stop();
});
test('configuration rejects unsafe destinations, identity and timing values',()=>{
 for(const overrides of [{baseURL:'https://key:secret@host'}, {baseURL:'file:///tmp'}, {baseURL:'https://host?a=key'}, {baseURL:'https://host/#fragment'}, {apiKey:'bad\nkey'}, {report:{...report(),generation:2147483648}}, {report:{...report(),capabilities:['task_identity']}}, {timeoutMs:0}, {retryMs:200,retryMaxMs:100}])assert.throws(()=>fixture(overrides),e=>e.code==='INVALID_CONFIGURATION');
});
