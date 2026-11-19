import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {createManagedShutdownScope} from './managed_shutdown_scope.mjs';
import {contracts,transform} from './patch_managed_model_reference_102.mjs';
const clean=()=>({durationMs:1,warnings:[]});
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
function fixture(){const calls=[];return {calls,scope:createManagedShutdownScope({enabled:()=>true,begin:()=>calls.push('begin'),finish:async value=>{assert.equal(value.confirmNativeStopped,true);assert.equal(value.verifyNativeStopped(),true);calls.push('finish');}})};}
test('legacy shutdown never begins drain or interprets close result',async()=>{
 const scope=createManagedShutdownScope();assert.equal(await scope.run(async()=>scope.record('legacy')),'legacy');
});
test('proof belongs to full outer close success and preserves its return value',async()=>{
 const {scope,calls}=fixture();
 assert.equal(await scope.run(async()=>{calls.push('native');scope.record(clean());await Promise.resolve();calls.push('joined');return 3;}),3);
 assert.deepEqual(calls,['begin','native','joined','finish']);
});
test('warnings, missing results, malformed results and duplicate closes retain ownership',async()=>{
 for(const results of [[],[{durationMs:1,warnings:['media-cleanup']}],[null],[{durationMs:-1,warnings:[]}],[{durationMs:NaN,warnings:[]}],[{durationMs:1}],[clean(),clean()]]){
  const {scope,calls}=fixture();await assert.rejects(scope.run(async()=>{for(const result of results)scope.record(result);}),{code:'NATIVE_SHUTDOWN_UNCONFIRMED'});
  assert.deepEqual(calls,['begin']);
 }
});
test('outer failure after inner clean result never confirms stopped work',async()=>{
 const {scope,calls}=fixture();const error=new Error('native cleanup failed');
 await assert.rejects(scope.run(async()=>{scope.record(clean());throw error;}),e=>e===error);assert.deepEqual(calls,['begin']);
});
test('result mutation before full join is rechecked',async()=>{
 const {scope,calls}=fixture();await assert.rejects(scope.run(async()=>{const result=clean();scope.record(result);await Promise.resolve();result.warnings.push('late cleanup');}),{code:'NATIVE_SHUTDOWN_UNCONFIRMED'});
 assert.deepEqual(calls,['begin']);
});
test('concurrent close cannot borrow an unrelated result',async()=>{
 const {scope,calls}=fixture(),gate=deferred();
 const first=scope.run(async()=>{await gate.promise;});
 await assert.rejects(scope.run(async()=>scope.record(clean())),{code:'SHUTDOWN_ALREADY_RUNNING'});
 scope.record(clean());gate.resolve();await assert.rejects(first,{code:'NATIVE_SHUTDOWN_UNCONFIRMED'});assert.deepEqual(calls,['begin']);
});
test('late detached cleanup cannot add proof after scope closed',async()=>{
 const {scope,calls}=fixture(),gate=deferred();let late;
 await scope.run(async()=>{late=gate.promise.then(()=>scope.record(clean()));scope.record(clean());});
 gate.resolve();await assert.rejects(late,{code:'NATIVE_SHUTDOWN_CLOSED'});assert.deepEqual(calls,['begin','finish']);
});
test('release failure is propagated rather than reported as clean shutdown',async()=>{
 const error=new Error('writer retained');
 const scope=createManagedShutdownScope({enabled:()=>true,begin(){},finish:async()=>{throw error;}});
 await assert.rejects(scope.run(async()=>scope.record(clean())),e=>e===error);
});
test('actual pinned outer shutdown rejects optional step failures before release',{skip:!process.env.OPENCLAW_NATIVE_FIXTURE_DIR},async()=>{
 const contract=contracts.find(c=>c.file==='server-shutdown-DqBhRLh6.mjs');
 const source=transform(fs.readFileSync(path.join(process.env.OPENCLAW_NATIVE_FIXTURE_DIR,contract.file),'utf8'),contract);
 const a=source.indexOf('async function runGatewayShutdownSteps('),b=source.indexOf('/** Startup failure',a);
 const c=source.indexOf('function runGatewayCloseSteps('),d=source.indexOf('/** Failed acquisition',c);
 assert.ok(a>=0&&b>a&&c>=0&&d>c);
 for(const failed of [false,true]){
  const {scope,calls}=fixture();
  const run=new Function('cmWithManagedNativeShutdown','markGatewayRestartTrace','measureGatewayRestartTrace','formatErrorMessage',
   source.slice(a,b)+source.slice(c,d)+';return runGatewayCloseSteps;')(scope.run,()=>{},async(_name,op)=>op(),String);
  const owner={stopConnectionDependentSidecars:async()=>{},connectionWork:{drain:async()=>{}},stopRegisteredGatewayLifetimeSidecars:async()=>{},stopRegisteredPostReadySidecars:async()=>{},runClosePrelude:async()=>{},sealAndJoinRegisteredSidecarStops:async()=>{}};
  const promise=run({owner,disposeTerminalSessions:async()=>{if(failed)throw new Error('terminal cleanup');},close:async()=>scope.record(clean()),onError:()=>{}});
  if(failed){await assert.rejects(promise,/Gateway shutdown did not complete cleanly/);assert.deepEqual(calls,['begin']);}
  else{await promise;assert.deepEqual(calls,['begin','finish']);}
 }
});
