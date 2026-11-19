import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {resolveManagedReference,prepareManagedRun,readCatalog,ManagedModelError,filterManagedModelCatalog} from './managed_model_reference.mjs';
import {contracts,transform,install,inspectArtifact} from './patch_managed_model_reference_102.mjs';
import {stripTurnPatch} from './managed_turn_patch_102.mjs';

const base='http://gateway.invalid/v1';
const key='test-owner-secret';
const historic='cm-provider-v1-'+'a'.repeat(64);
const profile=(id,credential=key)=>({baseUrl:base,apiKey:credential,models:[{id,contextWindow:8192,maxTokens:1024}]});
const catalog=()=>[{id:'new-model',stable_id:'cm-stable-1',aliases:['old-model','cm-model-v1-old'],
  provider_refs:[{provider_id:'old-provider',model_id:'old-model'},{provider_id:historic,model_id:'old-model'}]}];
const config=()=>({models:{providers:{'old-provider':profile('old-model'),'new-provider':profile('new-model')}}});
let previousEnv,previousFetch,calls,items;
test.beforeEach(()=>{
 previousEnv={...process.env}; previousFetch=globalThis.fetch; calls=0; items=catalog();
 process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL='1';
 process.env.CLAWMANAGER_LLM_BASE_URL=base; process.env.CLAWMANAGER_LLM_API_KEY=key;
 globalThis.fetch=async(url,options)=>{
  calls++; assert.equal(url,base+'/runtime/catalog'); assert.equal(options.redirect,'error');
  assert.equal(options.headers.Authorization,'Bearer '+key);
  return new Response(JSON.stringify({models:items}));
 };
});
test.afterEach(()=>{process.env=previousEnv;globalThis.fetch=previousFetch;});
test('restores same deployment without mutating params, config or transcript',async()=>{
 const cfg=config();
 const history=Object.freeze([{role:'user',content:'existing conversation'}]);
 const params=Object.freeze({config:cfg,provider:'old-provider',model:'old-model',messages:history,runId:'r',streamParams:{temperature:0.2}});
 const before=JSON.stringify(params),next=await prepareManagedRun(params);
 assert.equal(next.provider,'new-provider');assert.equal(next.model,'new-model');
 assert.equal(next.messages,history);assert.equal(next.streamParams,params.streamParams);
 assert.equal(JSON.stringify(params),before);assert.equal(calls,1);
});
test('restores missing normalized provider only by exact full identity',async()=>{
 const cfg={models:{providers:{'new-provider':profile('new-model')}}};
 const resolved=await resolveManagedReference(cfg,{provider:historic,model:'old-model'});
 assert.deepEqual(resolved,{provider:'new-provider',model:'new-model'});
 await assert.rejects(resolveManagedReference(cfg,{provider:historic,model:'wrong'}),e=>e.code==='MODEL_UNAVAILABLE');
});
test('revoked and deleted models fail with no positive permission cache',async()=>{
 await prepareManagedRun({config:config(),provider:'old-provider',model:'old-model'});
 items=[];
 await assert.rejects(prepareManagedRun({config:config(),provider:'old-provider',model:'old-model'}),e=>e instanceof ManagedModelError && e.code==='MODEL_UNAVAILABLE');
 assert.equal(calls,2);
});
test('ambiguous historical identity never selects first or auto',async()=>{
 items.push({...catalog()[0],id:'other-url',stable_id:'cm-stable-2'});
 await assert.rejects(resolveManagedReference(config(),{provider:'old-provider',model:'old-model'}),e=>e.code==='MODEL_UNAVAILABLE');
});
test('a wire alias ambiguous across deployments cannot be used as target',async()=>{
 items.push({id:'other',stable_id:'cm-stable-2',aliases:['new-model']});
 const cfg={models:{providers:{'new-provider':profile('new-model')}}};
 await assert.rejects(resolveManagedReference(cfg,{provider:historic,model:'old-model'}),e=>e.code==='MODEL_UNAVAILABLE');
});
test('custom provider with same URL but different key wins',async()=>{
 const cfg={models:{providers:{[historic]:profile('old-model','other')}}};
 const selection={provider:historic,model:'old-model'};
 assert.equal(await resolveManagedReference(cfg,selection),selection);assert.equal(calls,0);
});
test('custom URL, secret references and missing built-in routes stay native',async()=>{
 for(const value of [{...profile('old-model'),baseUrl:'https://other.invalid'}, {...profile('old-model'),apiKey:{source:'env',id:'CUSTOM'}}]) {
  const selection={provider:'old-provider',model:'old-model'};
  assert.equal(await resolveManagedReference({models:{providers:{'old-provider':value}}},selection),selection);
 }
 const builtin={provider:'anthropic',model:'old-model'};
 assert.equal(await resolveManagedReference(config(),builtin),builtin);
 assert.equal(calls,0);
});
test('disabled protocol and locked/native auth overrides are unchanged',async()=>{
 for(const extra of [{modelSelectionLocked:true},{authProfileId:'foreign:profile'},{userLockedAuthProfileId:'foreign:profile'}]) {
  const params={config:config(),provider:'old-provider',model:'old-model',...extra};
  assert.equal(await prepareManagedRun(params),params);
 }
 process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL='';
 const params={config:config(),provider:'old-provider',model:'old-model'};
 assert.equal(await prepareManagedRun(params),params);assert.equal(calls,0);
});
test('stable and slash aliases work only inside an owned route',async()=>{
 items[0].aliases.push('vendor/old');
 for(const model of ['cm-stable-1','vendor/old']) {
  assert.equal((await resolveManagedReference(config(),{provider:'old-provider',model})).model,'new-model');
 }
});
test('catalog errors are bounded and sanitized',async()=>{
 for(const body of ['not-json',JSON.stringify({models:[{id:'missing-stable'}]}),'x'.repeat(1024*1024+1)]) {
  globalThis.fetch=async()=>new Response(body);
  await assert.rejects(readCatalog(),e=>e.code==='MODEL_CATALOG_UNAVAILABLE'&&!e.message.includes(key)&&!e.message.includes(base));
 }
 globalThis.fetch=async()=>new Response(key,{status:403});
 await assert.rejects(readCatalog(),e=>e.code==='MODEL_CATALOG_UNAVAILABLE'&&!e.message.includes(key));
});
test('caller cancellation is propagated before dispatch',async()=>{
 const controller=new AbortController();controller.abort();
 await assert.rejects(prepareManagedRun({config:config(),provider:'old-provider',model:'old-model',abortSignal:controller.signal}));
 assert.equal(calls,0);
});
test('catalog body read is included in timeout',async()=>{
 globalThis.fetch=async(_url,{signal})=>new Response(new ReadableStream({
  start(controller){signal.addEventListener('abort',()=>controller.error(new Error('deadline')),{once:true});},
  cancel(){}
 }));
 const start=Date.now();
 await assert.rejects(readCatalog(),e=>e.code==='MODEL_CATALOG_UNAVAILABLE');
 assert.ok(Date.now()-start<6000);
});
const fixture=process.env.OPENCLAW_NATIVE_FIXTURE_DIR;
test('picker filters revoked models without caching or mutating native metadata',async()=>{
 const row=Object.freeze({provider:'new-provider',id:'new-model',name:'Native name',available:false,manualSelectionAllowed:false});
 const payload=Object.freeze({models:Object.freeze([row]),sessionId:'old-session',model:'old-model'});
 const result=await filterManagedModelCatalog(config(),payload);
 assert.deepEqual(result.models,[row]);assert.equal(result.models[0],row);
 items=[];
 const revoked=await filterManagedModelCatalog(config(),payload);
 assert.deepEqual(revoked.models,[]);assert.equal(revoked.model,'old-model');
 assert.deepEqual(payload.models,[row]);assert.equal(calls,2);
});
test('picker leaves custom credentials and built-ins alone, never inserts models',async()=>{
 const cfg=config();cfg.models.providers.foreign=profile('new-model','other-key');
 const rows=[{provider:'foreign',id:'new-model'},{provider:'anthropic',id:'claude'},{provider:'new-provider',id:'new-model'}];
 items.push({id:'not-configured',stable_id:'cm-new'});
 const result=await filterManagedModelCatalog(cfg,{models:rows});
 assert.deepEqual(result.models,rows);
 items=[];
 assert.deepEqual((await filterManagedModelCatalog(cfg,{models:rows})).models,rows.slice(0,2));
 const custom={models:rows.slice(0,2)};
 assert.equal(await filterManagedModelCatalog(cfg,custom),custom);assert.equal(calls,2);
});
test('picker rejects ambiguity, unconfigured routes and exact-only wire mismatches',async()=>{
 const cfg=config();
 const payload={models:[{provider:'new-provider',id:'new-model'},{provider:historic,id:'old-model'},{provider:'new-provider',id:'ghost'}]};
 assert.deepEqual((await filterManagedModelCatalog(cfg,payload)).models,[payload.models[0]]);
 items.push({id:'different-url',stable_id:'cm-other',aliases:['new-model']});
 assert.deepEqual((await filterManagedModelCatalog(cfg,payload)).models,[]);
 items=[{id:'new-name',stable_id:'cm-stable-1',provider_refs:[{provider_id:'new-provider',model_id:'new-model'}]}];
 assert.deepEqual((await filterManagedModelCatalog(cfg,payload)).models,[]);
});
test('picker refuses unavailable catalogs instead of returning stale grants',async()=>{
 globalThis.fetch=async()=>new Response('private',{status:500});
 await assert.rejects(filterManagedModelCatalog(config(),{models:[{provider:'new-provider',id:'new-model'}]}),e=>e.code==='MODEL_CATALOG_UNAVAILABLE');
});
test('picker disabled and metadata without models retain original behavior',async()=>{
 const metadata={sessionId:'s'};
 assert.equal(await filterManagedModelCatalog(config(),metadata),metadata);
 process.env.CLAWMANAGER_MODEL_REFERENCE_PROTOCOL='';
 const payload={models:[{provider:'new-provider',id:'new-model'}]};
 assert.equal(await filterManagedModelCatalog(config(),payload),payload);assert.equal(calls,0);
});
test('picker cancellation is propagated',async()=>{
 const controller=new AbortController();controller.abort();
 await assert.rejects(filterManagedModelCatalog(config(),{models:[{provider:'new-provider',id:'new-model'}]},controller.signal));
});
test('picker leaves explicit account overrides and locked sessions native',async()=>{
 const payload={models:[{provider:'new-provider',id:'new-model'}]};items=[];
 for(const selection of [{authProfileId:'personal'},{modelSelectionLocked:true},{userLockedAuthProfileId:'shared'}]) {
  assert.equal(await filterManagedModelCatalog(config(),payload,undefined,selection),payload);
 }
 assert.equal(calls,0);
});
test('native metadata handler checks current authority after catalog read',{skip:!fixture},async()=>{
 const contract=contracts.find(c=>c.file.startsWith('chat-metadata-handler'));
 const wrapped=extract(transform(sourceFor(contract),contract),'handleChatMetadataRequest');
 const cfg=config(), row={provider:'new-provider',id:'new-model',manualSelectionAllowed:false};
 let current=true, released=0, response;
 class AuthorityChanged extends Error { constructor(){super();this.error={code:'FORBIDDEN'};} }
 const scope={agentId:'main',assertCurrent(){if(!current)throw new AuthorityChanged();},release(){released++;}};
 const dependencies={assertValidParams:()=>true,validateChatMetadataParams:{},resolveChatMetadataReadParams:async()=>scope,
  preparePersonalModelAccountSelection:()=>{throw new Error('unexpected personal account');},SESSION_READ_SCOPE:'read',
  cmFilterManagedModelCatalog:filterManagedModelCatalog,cmIsManagedModelError:e=>e instanceof ManagedModelError,
  prepareOperatorModelPresentation:()=>({forAgent:()=>({metadata:m=>({...m,models:m.models.filter(r=>r.manualSelectionAllowed!==false)})})}),
  projectModelFastModeCatalog:m=>m,SessionMutationAuthorizationChangedError:AuthorityChanged,ModelAccountConnectAuthorityError:class extends Error {},
  ErrorCodes:{UNAVAILABLE:'UNAVAILABLE',FORBIDDEN:'FORBIDDEN'},errorShape:(code,message)=>({code,message})};
 const run=new Function(...Object.keys(dependencies),'return ('+wrapped+')')(...Object.values(dependencies));
 const options={params:{},context:{getRuntimeConfig:()=>cfg,readChatMetadata:async()=>({models:[row]})},respond:(...args)=>{response=args;}};
 await run(options);assert.equal(response[0],true);assert.deepEqual(response[1].models,[]);
 globalThis.fetch=async()=>{current=false;return Response.json({models:items});};
 await run(options);assert.equal(response[0],false);assert.equal(response[2].code,'FORBIDDEN');assert.equal(released,2);
 current=true;globalThis.fetch=async()=>new Response('',{status:503});
 await run(options);assert.equal(response[0],false);assert.equal(response[2].code,'UNAVAILABLE');assert.equal(released,3);
});
test('native models.list filters prepared cache and refuses configuration races',{skip:!fixture},async()=>{
 const contract=contracts.find(c=>c.file.startsWith('models-'));
 const source=transform(sourceFor(contract),contract);
 const start=source.indexOf('const modelsHandlers ='),end=source.lastIndexOf('//#endregion');
 assert.ok(start>=0&&end>start);
 const cfg=config(),rows=[{provider:'new-provider',id:'new-model'},{provider:'foreign',id:'custom'}];
 let currentCfg=cfg,response,released=0;
 const scope={agentId:'main',assertCurrent(){},release(){released++;}};
 const dependencies={assertValidParams:()=>true,validateModelsListParams:{},resolveChatMetadataReadParams:async()=>scope,
  preparePersonalModelAccountSelection:()=>{throw new Error('unexpected profile');},SESSION_READ_SCOPE:'read',
  tryResolveAmbientOwnerAgentId:()=> 'main',resolveAgentIdOrRespondError:()=>({agentId:'main'}),normalizeOptionalString:v=>v,
  authorizeCurrentOperatorRoleScopes:()=>undefined,roleScopesAllow:()=>true,refreshExpiredPreparedModelCatalog:()=>{},
  hasGatewayClientCap:()=>true,GATEWAY_CLIENT_CAPS:{MODEL_SELECTION_POLICY:'policy'},buildModelsListResult:()=>{throw new Error('must use prepared cache');},
  projectSessionModelCatalog:(_s,m)=>m,cmFilterManagedModelCatalog:filterManagedModelCatalog,cmIsManagedModelError:e=>e instanceof ManagedModelError,
  prepareOperatorModelPresentation:()=>({forAgent:()=>({catalog:m=>m})}),projectModelFastModeCatalog:m=>m,
  applyRemoteModelCatalogUpdate:()=>{},UnknownModelCatalogProviderError:class extends Error {},SessionMutationAuthorizationChangedError:class extends Error {},
  PreparedModelRuntimePublicationSupersededError:class extends Error {},ModelAccountConnectAuthorityError:class extends Error {},
  ErrorCodes:{UNAVAILABLE:'UNAVAILABLE'},errorShape:(code,message)=>({code,message})};
 const run=new Function(...Object.keys(dependencies),source.slice(start,end)+'return modelsHandlers["models.list"];')(...Object.values(dependencies));
 const options={params:{},context:{getRuntimeConfig:()=>currentCfg,readPreparedModelsList:async()=>({models:rows})},respond:(...a)=>{response=a;}};
 items=[];
 await run(options);assert.equal(response[0],true);assert.deepEqual(response[1].models,[rows[1]]);assert.equal(calls,1);
 globalThis.fetch=async()=>{currentCfg=structuredClone(cfg);return Response.json({models:items});};
 await run(options);assert.equal(response[0],false);assert.equal(response[2].code,'UNAVAILABLE');assert.equal(released,2);
 assert.equal(rows.length,2);
});
test('native startup filters metadata before publication, not chat history',{skip:!fixture},async()=>{
 const contract=contracts.find(c=>c.file.startsWith('chat-history-handler'));
 const source=transform(sourceFor(contract),contract);
 const start=source.indexOf('\t\tlet startupMetadata =');
 const end=source.indexOf('\t\tconst { sessionModelCatalog, defaultModelCatalog }',start);
 assert.ok(start>=0&&end>start);
 const fragment=source.slice(start,end);
 const cfg=config(), metadata={models:[{provider:'new-provider',id:'new-model'}],sessionId:'s'};
 let response;
 const run=new Function('cmFilterManagedModelCatalog','cmIsManagedModelError','errorShape','ErrorCodes','respondChatHistoryUnavailable',
  'return async function(method,startupProjection,cfg,context,respond,signal,sessionMutationAuthorization,entry){'+fragment+'return startupMetadata;}')(
  filterManagedModelCatalog,e=>e instanceof ManagedModelError,(code,message)=>({code,message}),{UNAVAILABLE:'UNAVAILABLE'},(_m,respond,message)=>respond(false,undefined,{message}));
 items=[];
 const args=[{metadata},cfg,{getRuntimeConfig:()=>cfg},(...a)=>{response=a;}];
 assert.deepEqual((await run('chat.startup',...args)).models,[]);
 assert.equal(await run('chat.history',...args),undefined);assert.equal(calls,1);
 assert.equal(metadata.models.length,1);
 globalThis.fetch=async()=>new Response('',{status:500});
 assert.equal(await run('chat.startup',...args),undefined);assert.equal(response[0],false);assert.equal(response[2].code,'UNAVAILABLE');
});
test('legacy overlay leaves old core unchanged but rejects unknown future core',()=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'cm-openclaw-legacy-'));
 try {
  const sentinel=path.join(root,'native.js');
  fs.writeFileSync(sentinel,'user native bytes');
  const script=fileURLToPath(new URL('./patch_managed_model_reference_102.mjs',import.meta.url));
  for(const [version,status] of [['2026.8.1',0],['2027.1.1',1]]) {
   fs.writeFileSync(path.join(root,'package.json'),JSON.stringify({version}));
   const run=spawnSync(process.execPath,[script,'--verify','--allow-legacy'],{env:{...process.env,OPENCLAW_PACKAGE_ROOT:root},encoding:'utf8'});
   assert.equal(run.status,status,run.stderr);
   assert.equal(fs.readFileSync(sentinel,'utf8'),'user native bytes');
   if(status===0) assert.match(run.stdout,/restoration unavailable/);
  }
 } finally {fs.rmSync(root,{recursive:true,force:true});}
});
function sourceFor(contract) {
 const file=contract.file.startsWith('embedded-agent-')&&!fs.existsSync(path.join(fixture,contract.file))?'openclaw-embedded-agent.mjs':contract.file;
 return fs.readFileSync(path.join(fixture,file),'utf8');
}
test('pinned native patches validate, install idempotently, and reject drift',{skip:!fixture},()=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'cm-openclaw-native-'));
 try {
  fs.mkdirSync(path.join(root,'dist'));fs.writeFileSync(path.join(root,'package.json'),JSON.stringify({version:'2026.10.2'}));
  for(const contract of contracts) {
   const source=sourceFor(contract),patched=transform(source,contract);
   assert.equal(transform(patched,contract),patched);
   assert.equal(transform(stripTurnPatch(patched,contract.file),contract),patched,'previous reference-only patch upgrades without source drift');
   assert.throws(()=>transform(source+'\n// drift',contract),/Unsupported/);
   fs.writeFileSync(path.join(root,'dist',contract.file),source);
  }
  install(root);install(root);install(root,true);
 } finally {fs.rmSync(root,{recursive:true,force:true});}
});
function installedFixture(){
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'cm-openclaw-artifact-'));
 fs.mkdirSync(path.join(root,'dist'));fs.writeFileSync(path.join(root,'package.json'),JSON.stringify({version:'2026.10.2'}));
 for(const contract of contracts)fs.writeFileSync(path.join(root,'dist',contract.file),sourceFor(contract));
 install(root);return root;
}
test('artifact inventory is deterministic and inspection does not claim activation',{skip:!fixture},()=>{
 const root=installedFixture();
 try{
  const manifestPath=path.join(root,'dist','clawmanager-managed-artifact.json'),before=fs.readFileSync(manifestPath,'utf8');
  const manifest=JSON.parse(before);assert.equal(Object.keys(manifest.files).length,17);assert.equal(manifest.release_accepted,false);
  const report=inspectArtifact(root);assert.equal(report.integrity_verified,true);assert.equal(report.activation_ready,false);assert.equal(report.release_accepted,false);assert.match(report.artifact_sha256,/^[a-f0-9]{64}$/);
  assert.equal(report.node_version_matches,process.version==='v24.21.0');
  assert.equal(inspectArtifact(root).artifact_sha256,report.artifact_sha256);assert.equal(fs.readFileSync(manifestPath,'utf8'),before);
  install(root);assert.equal(fs.readFileSync(manifestPath,'utf8'),before);
 }finally{fs.rmSync(root,{recursive:true,force:true});}
});
test('artifact inspection rejects missing, forged and drifted files without repairing',{skip:!fixture},()=>{
 const root=installedFixture();
 try{
  const manifestPath=path.join(root,'dist','clawmanager-managed-artifact.json'),original=fs.readFileSync(manifestPath,'utf8');
  const manifest=JSON.parse(original);manifest.release_accepted=true;const forged=JSON.stringify(manifest);fs.writeFileSync(manifestPath,forged);
  assert.throws(()=>inspectArtifact(root),/manifest differs/);assert.equal(fs.readFileSync(manifestPath,'utf8'),forged);
  fs.unlinkSync(manifestPath);assert.throws(()=>inspectArtifact(root));assert.equal(fs.existsSync(manifestPath),false);
  fs.writeFileSync(manifestPath,original);
  const helper=path.join(root,'dist','runtime_task_guard.mjs');fs.appendFileSync(helper,'\n// drift');assert.throws(()=>inspectArtifact(root),/guard differs/);assert.ok(fs.readFileSync(helper,'utf8').endsWith('// drift'));
 }finally{fs.rmSync(root,{recursive:true,force:true});}
});
test('release gate refuses activation even when artifact integrity passes',{skip:!fixture},()=>{
 const root=installedFixture();
 try{
  const script=fileURLToPath(new URL('./patch_managed_model_reference_102.mjs',import.meta.url));
  const run=spawnSync(process.execPath,[script,'--inspect','--require-activation-ready'],{env:{...process.env,OPENCLAW_PACKAGE_ROOT:root},encoding:'utf8'});
  assert.equal(run.status,2,run.stderr);const report=JSON.parse(run.stdout);assert.equal(report.integrity_verified,true);assert.equal(report.activation_ready,false);
  for(const args of [['--require-activation-ready'],['--inspect','--patch'],['--inspect','--allow-legacy']]){
   const rejected=spawnSync(process.execPath,[script,...args],{env:{...process.env,OPENCLAW_PACKAGE_ROOT:root},encoding:'utf8'});assert.notEqual(rejected.status,0);
  }
 }finally{fs.rmSync(root,{recursive:true,force:true});}
});
test('renewal helper is installed and cannot drift outside the artifact manifest',{skip:!fixture},()=>{
 const root=installedFixture();
 try {
  const helper=path.join(root,'dist','runtime_capability_renewal.mjs');
  const manifest=JSON.parse(fs.readFileSync(path.join(root,'dist','clawmanager-managed-artifact.json'),'utf8'));
  assert.ok(manifest.files['runtime_capability_renewal.mjs']);assert.ok(fs.existsSync(helper));
  fs.appendFileSync(helper,'\n// drift');assert.throws(()=>inspectArtifact(root),/Capability renewal helper differs/);
  fs.unlinkSync(helper);assert.throws(()=>inspectArtifact(root));assert.equal(fs.existsSync(helper),false);
 }finally{fs.rmSync(root,{recursive:true,force:true});}
});
function extract(source,name) {
 const start=source.indexOf('async function '+name+'(');
 assert.ok(start>=0);
 const tail=source.slice(start);
 const next=tail.slice(1).search(/\n(?:async )?function |\n\/\/#endregion/);
 assert.ok(next>=0);
 return tail.slice(0,next+1);
}
test('actual native fallback wrapper resolves before invoking dispatcher',{skip:!fixture},async()=>{
 const contract=contracts.find(c=>c.file.startsWith('model-fallback-runner'));
 const wrapped=extract(transform(sourceFor(contract),contract),'runWithModelFallback');
 let invoked=0;
 const run=new Function('cmPrepareManagedRun','runWithModelFallbackInternal','flushDeferredSessionSuspension','shouldDiscardDeferredSessionSuspension','cmWithManagedTurn',
  'return ('+wrapped+')')(prepareManagedRun,async(params)=>{invoked++;return {provider:params.provider,model:params.model};},()=>{},()=>true,(params,operation)=>operation(params));
 const result=await run({cfg:config(),provider:'old-provider',model:'old-model'});
 assert.deepEqual(result,{provider:'new-provider',model:'new-model'});
 items=[];
 await assert.rejects(run({cfg:config(),provider:'old-provider',model:'old-model'}),e=>e.code==='MODEL_UNAVAILABLE');
 assert.equal(invoked,1);
});
