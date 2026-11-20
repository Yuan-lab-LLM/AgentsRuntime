import {AsyncLocalStorage} from 'node:async_hooks';
import {beginManagedShutdown,finishManagedShutdown} from './managed_turn_bridge.mjs';
import {capabilityRenewalReleaseAccepted} from './runtime_capability_renewal.mjs';
const fail=code=>{const e=new Error(code);e.code=code;throw e;};
const clean=result=>result&&typeof result==='object'&&Number.isFinite(result.durationMs)&&result.durationMs>=0&&Array.isArray(result.warnings)&&result.warnings.length===0;
export function createManagedShutdownScope({enabled=()=>false,begin,finish}={}){
 const scope=new AsyncLocalStorage();let running=false;
 async function run(operation){
  if(enabled()!==true)return operation();
  if(running||scope.getStore())fail('SHUTDOWN_ALREADY_RUNNING');
  running=true;const state={results:[],closed:false};
  try{
   begin();
   return await scope.run(state,async()=>{
    const value=await operation();
    state.closed=true;
    if(state.results.length!==1||!clean(state.results[0]))fail('NATIVE_SHUTDOWN_UNCONFIRMED');
    await finish({confirmNativeStopped:true,verifyNativeStopped:()=>state.results.length===1&&clean(state.results[0])});
    return value;
   });
  }finally{state.closed=true;running=false;}
 }
 function record(result){
  const current=scope.getStore();
  if(!current)return result;
  if(current.closed)fail('NATIVE_SHUTDOWN_CLOSED');
  current.results.push(result);
  return result;
 }
 return Object.freeze({run,record});
}
const nativeScope=createManagedShutdownScope({
 enabled:()=>capabilityRenewalReleaseAccepted&&process.env.CLAWMANAGER_OPENCLAW_TASK_PROTOCOL==='1'&&process.env.CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL==='1',
 begin:beginManagedShutdown,finish:finishManagedShutdown,
});
export const withManagedNativeShutdown=nativeScope.run;
export const recordManagedNativeCloseResult=nativeScope.record;
