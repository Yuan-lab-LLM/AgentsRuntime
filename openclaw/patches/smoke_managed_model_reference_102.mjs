// Run only inside a disposable container/install. No real provider calls.
import assert from 'node:assert/strict';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {install,contracts} from './patch_managed_model_reference_102.mjs';
if(!process.argv.includes('--disposable')) throw new Error('Requires --disposable installation');
const root=process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
install(root);install(root,true);
for(const contract of contracts) await import(pathToFileURL(path.join(root,'dist',contract.file)).href);
const {t:runWithModelFallback}=await import(pathToFileURL(path.join(root,'dist','model-fallback-runner-COVxfi1J.mjs')).href);
const {n:resolveModelAsync}=await import(pathToFileURL(path.join(root,'dist','model-IiOP36F5.mjs')).href);
const {ManagedModelError}=await import(pathToFileURL(path.join(root,'dist','clawmanager-managed-model-reference.mjs')).href);
const {RuntimeTaskError}=await import(pathToFileURL(path.join(root,'dist','runtime_task_guard.mjs')).href);
const {withManagedTurn}=await import(pathToFileURL(path.join(root,'dist','managed_turn_bridge.mjs')).href);
const base='http://gateway.invalid/v1',key='test-key';
process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL='1';
process.env.CLAWMANAGER_LLM_BASE_URL=base;process.env.CLAWMANAGER_LLM_API_KEY=key;
const profile=id=>({baseUrl:base,apiKey:key,api:'openai-completions',models:[{id,name:id,contextWindow:8192,maxTokens:1024,reasoning:false,input:['text'],cost:{input:0,output:0,cacheRead:0,cacheWrite:0}}]});
const cfg={models:{providers:{'old-provider':profile('old-model'),'new-provider':profile('new-model'),'auto':profile('auto')}}};
const params={cfg,provider:'old-provider',model:'old-model',fallbacksOverride:['auto/auto'],skipAuthProfileRuntime:true};
let catalog=[{id:'new-model',stable_id:'cm-stable-1',aliases:['old-model'],provider_refs:[{provider_id:'old-provider',model_id:'old-model'}]}];
const originalFetch=globalThis.fetch;
globalThis.fetch=async(url)=>{assert.equal(url,base+'/runtime/catalog');return new Response(JSON.stringify({models:catalog}));};
let calls=0;
try {
 const resolved=await resolveModelAsync('old-provider','old-model',undefined,cfg,{skipAgentDiscovery:true});
 assert.equal(resolved.logicalRef.provider,'new-provider');
 assert.equal(resolved.logicalRef.model,'new-model');
 assert.equal(resolved.model.id,'new-model');
 assert.equal(resolved.model.contextWindow,8192);
 const result=await runWithModelFallback({...params,run:async(provider,model)=>{calls++;assert.equal(provider,'new-provider');assert.equal(model,'new-model');return 'ok';}});
 assert.equal(result.result,'ok');assert.equal(calls,1);
 // Revocation before entry must dispatch nothing, despite an available fallback.
 catalog=[];
 await assert.rejects(resolveModelAsync('old-provider','old-model',undefined,cfg,{skipAgentDiscovery:true}),e=>e.code==='MODEL_UNAVAILABLE');
 await assert.rejects(runWithModelFallback({...params,run:async()=>{calls++;return 'bad';}}),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(calls,1);
 // A terminal reference error originating deeper in native dispatch must not
 // be reclassified as a provider error and sent to the next fallback.
 catalog=[{id:'new-model',stable_id:'cm-stable-1',aliases:['old-model'],provider_refs:[]}];
 await assert.rejects(runWithModelFallback({...params,run:async()=>{calls++;throw new ManagedModelError('当前模型不可用');}}),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(calls,2);
 for(const code of ['MODEL_UNAVAILABLE','MODEL_AUTHORIZATION_UNAVAILABLE','ABORTED']) {
  const before=calls;
  await assert.rejects(runWithModelFallback({...params,run:async()=>{calls++;throw new RuntimeTaskError(code);}}),e=>e instanceof RuntimeTaskError&&e.code===code);
  assert.equal(calls,before+1,'task cancellation/auth failure must not fall back to another provider');
 }
 // Enable the gated native bridge only inside this disposable test process.
 process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL='1';process.env.CLAWMANAGER_GATEWAY_GENERATION='7';
 const tasks=[];let revoked=false,cleaned=false;
 globalThis.fetch=async(url,options)=>{
  if(url.endsWith('/catalog'))return Response.json({models:catalog});
  const body=JSON.parse(options.body);tasks.push({url,body});
  if(url.endsWith('/check'))return revoked?new Response('',{status:403}):Response.json({revoked:false,finished:false});
  assert.ok(url.endsWith('/finish'));if(body.cancelled)assert.equal(cleaned,true);
  return Response.json({accepted:true});
 };
 const identity={sessionId:'native-session',runId:'native-turn'};
 const success=await runWithModelFallback({...params,...identity,run:async(provider,model)=>{
  const selected=await resolveModelAsync(provider,model,undefined,cfg,{skipAgentDiscovery:true});
  assert.equal(selected.model.headers['X-ClawManager-Task-ID'],identity.runId);
  assert.equal(selected.model.headers['X-ClawManager-Session-ID'],identity.sessionId);
  return 'guarded';
 }});
 assert.equal(success.result,'guarded');assert.equal(tasks.filter(t=>t.url.endsWith('/finish')).length,1);
 const before=calls;
 await assert.rejects(runWithModelFallback({...params,...identity,runId:'native-revoked',run:async(provider,model)=>{
  calls++;
  const selected=await resolveModelAsync(provider,model,undefined,cfg,{skipAgentDiscovery:true});
  assert.equal(selected.model.headers['X-ClawManager-Task-ID'],'native-revoked');
  return withManagedTurn({config:cfg,provider,model},async({abortSignal})=>{
   revoked=true;
   await new Promise(resolve=>abortSignal.addEventListener('abort',resolve,{once:true}));
   cleaned=true;return 'must not succeed';
  },'config','compaction');
 }}),e=>e instanceof RuntimeTaskError&&e.code==='MODEL_UNAVAILABLE');
 assert.equal(calls,before+1);assert.equal(tasks.at(-1).body.cancelled,true);
 assert.ok(tasks.filter(t=>t.body.task_id==='native-revoked').every(t=>t.body.session_id==='native-session'));
 console.log('Native modules, same-turn task headers, descendant cancellation, cleanup receipt and terminal no-fallback passed');
} finally {globalThis.fetch=originalFetch;}
