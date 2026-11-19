import test from 'node:test';
import assert from 'node:assert/strict';
import {createBridge} from './bridge.mjs';
import {nativeIdentity} from './native-client.mjs';

for (const [accepted, failed, foreign, transient] of [[true,false,false,false],[false,false,false,false],[true,true,false,false],[true,false,true,false],[true,false,false,true]]) {
 test(`native delivery requires receipt, owned input and successful execution: ${accepted}/${failed}/${foreign}/${transient}`, async()=>{
  let receive; const events=[];
  const envelope={taskId:'team-12-task-3',rootTaskId:'team-12-task-3',messageId:'original-input',text:'work'};
  const ids=nativeIdentity({teamId:'12',memberId:'worker',taskId:envelope.taskId,messageId:envelope.messageId,generation:2});
  const runtime={withActiveEnvelope:async(_,fn)=>{await fn();return {completed:accepted};},isTaskTerminal:async()=>true,isActiveTaskCompleted:()=>accepted,status:async()=>({currentSourceMessageId:'original-input'})};
  const protocol={readChannelConfig:()=>({enabled:true,teamId:'12',memberId:'worker',embeddedTimeoutSeconds:10}),createRuntime:()=>runtime,
   RedisClient:class {async connect(){}close(){}},eventsKey:()=> 'events',xaddJson:async(_,__,event)=>events.push(event),taskEvent:(_,event,envelope,p)=>({event,...p}),
   workflowReminderIsStale:async()=>false,isContextOnlyEnvelope:()=>true,appendLeaderTeamContext:async t=>t,appendRedisTeamCompletionGuidance:t=>t,
   startAssignmentActivityObserver:async()=>({stop:async()=>{}}),startConsumer:async(_,fn)=>{receive=fn;return{stop:async()=>{}}}};
  const native={admit:async()=>{},api:async p=>({data:p.endsWith('/inbox')?[]:{}}),interrupt:async()=>{},
   readLog:async(_,cursor,observe)=>{if(cursor)return cursor;
    observe({type:'session.inbox.delivered',created:Date.now(),durable:{seq:1},data:{inboxID:foreign?'another-input':ids.message}});
    observe({type:'session.reasoning.ended',created:Date.now(),durable:{seq:2},data:{text:'private-reasoning-canary'}});
    observe({type:'session.text.ended',created:Date.now(),durable:{seq:3},data:{text:'Full final public answer',assistantMessageID:'msg_actual_answer'}});
    observe({type:failed?'session.execution.failed':'session.execution.succeeded',created:Date.now(),durable:{seq:4},data:{}});return 4;}};
  if(transient){let reads=0;native.inspectTurn=async()=>{if(!reads++)throw Error("temporary read failure");return {consumed:true,failed:false,observed:{mtimeMs:Date.now(),sessionCursor:"actual-final-record"},publicText:{recordId:"actual-final-record",text:"Full final public answer",at:new Date().toISOString()}};};}
  const bridge=await createBridge({protocol,native,environment:{CLAWMANAGER_GATEWAY_GENERATION:'2'}});
  try {await bridge.start();if(failed&&!foreign)await assert.rejects(receive(envelope),/execution failed/);else await receive(envelope);
   const delivery=events.filter(e=>e.eventKind==='delivery_supplement');
   assert.equal(delivery.length,accepted&&!failed&&!foreign?1:0);
   assert.ok(!JSON.stringify(events).includes('private-reasoning-canary'));
   if(delivery.length){assert.equal(delivery[0].rootTaskTerminal,false);assert.equal(delivery[0].stateEffect,'none');assert.equal(delivery[0].text,'Full final public answer');}
  } finally {await bridge.stop();}
 });
}
