import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { DshHost, sessionIdentity, turnFacts } from './host.mjs';
import { nativeIdentity } from '../../opencode/team/native-client.mjs';

const identity = { teamId:'1',memberId:'worker',taskId:'team-1-task-2',messageId:'in-1',generation:3 };
test('busy steering enters the owned native next-step queue without another agent',async()=>{
 const messages=[],events=[];
 const host=new DshHost({ctx:{sessions:{flush:async()=>true},sessionQuery:{observeSession:async()=>({events,[Symbol.dispose](){}})}},directory:'/unused',freezeMessage:x=>x,installModelSelection(){}});
 host.current={identity,ids:nativeIdentity(identity),id:sessionIdentity(identity),agent:{status:'running',session:{},inbox:{nextStep:messages},steer:m=>messages.push(m)}};
 const value=await host.steer(identity,'reminder','notice');await host.steer(identity,'reminder','notice');
 assert.equal(messages.length,1);assert.equal(messages[0].source.kind,'clawmanager-team-context');
 events.push({type:'user/message',data:messages[0]});
 assert.equal((await host.observeContext({nativeMessageId:value.nativeMessageId},identity)).state,'native_context_consumed');
 host.current.agent.status='idle';assert.equal(await host.steer(identity,'late','other'),undefined);
});
test('DSH input is stable, response loss does not duplicate, and generations/tools are fenced', async () => {
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'dsh-host-'));let calls=0,flushFailure=false;
 const sessions=new Map(),agents=new Map();
 const ctx={agentPresets:{mount:async()=>{}},sessions:{flush:async()=>{if(flushFailure)throw Error('lost checkpoint');return true;}},
  sessionQuery:{observeSession:async id=>{if(!sessions.has(id))throw Object.assign(Error('missing'),{code:'SESSION_QUERY_SESSION_NOT_FOUND'});return {header:{cwd:'/workspace'},events:sessions.get(id),[Symbol.dispose](){}};}},
  agents:{get:id=>agents.get(id),create:async({sessionId})=>{sessions.set(sessionId,[]);const agent={id:sessionId,status:'idle',session:{id:sessionId},inbox:{nextTurn:[],nextStep:[]},followup:m=>{calls++;sessions.get(sessionId).push({type:'agent/inbox/spliced',data:{inserted:[m]}});},cancel(){},whenIdle:async()=>{}};agents.set(sessionId,agent);return{agent,dispose:async()=>{}};},resume:async({resumeSessionId})=>({agent:agents.get(resumeSessionId),dispose:async()=>{}})}};
 const host=new DshHost({ctx,directory,freezeMessage:x=>x,installModelSelection(){}});
 try{
  await host.admit(identity,'/workspace','original');await host.admit(identity,'/workspace','original');assert.equal(calls,1);
  await assert.rejects(host.admit({...identity,generation:4},'/workspace','original'),/Conflicting|retired/);
  await assert.rejects(host.admit(identity,'/workspace','changed'),/Conflicting/);
  const agent=agents.get(sessionIdentity(identity));
  await assert.rejects(host.toolIdentity({agent:{},callId:'old'}),/current native/);
  sessions.get(agent.id).push({type:'user/message',data:{source:{kind:'clawmanager-team',inputId:nativeIdentity(identity).message,teamId:'1',memberId:'worker',generation:3}}});
  assert.equal((await host.toolIdentity({agent,callId:'native-1'})).generation,3);
  sessions.get(agent.id).push({type:'user/message',data:{source:{kind:'agent-instructions'}}});
  assert.equal((await host.toolIdentity({agent,callId:'with-native-instructions'})).generation,3);
  sessions.get(agent.id).push({type:'user/message',data:{source:{kind:'user'}}});
  await assert.rejects(host.toolIdentity({agent,callId:'human-session'}),/not owned/);
  flushFailure=true;const next={...identity,messageId:'in-2'};
  await assert.rejects(host.admit(next,'/workspace','second'),/checkpoint/);flushFailure=false;
  await host.admit(next,'/workspace','second');assert.equal(calls,2);
  sessions.set(sessionIdentity(next),[]);
  await assert.rejects(host.admit(next,'/workspace','second'),/uncertain/);assert.equal(calls,2);
  const ambiguous={...identity,messageId:'reserved-only'};await host.journal(nativeIdentity(ambiguous).message,{digest:'wrong',generation:3});
  await assert.rejects(host.admit(ambiguous,'/workspace','third'),/Conflicting/);
  await host.close();
  const restored=new DshHost({ctx,directory,freezeMessage:x=>x,installModelSelection(){}});
  await restored.admit(identity,'/workspace','original');assert.equal(calls,2);await restored.close();
 }finally{await host.close();await fs.rm(directory,{recursive:true,force:true});}
});

test('public answer observation never exports reasoning, failures, or another input',()=>{
 const events=[{type:'user/message',seq:0,time:1,data:{source:{kind:'clawmanager-team',inputId:'input'}}},
  {type:'assistant/message',seq:1,time:2,data:{message:{id:'answer',content:[{type:'reasoning',text:'private'},{type:'text',text:'public'}]}}},
  {type:'turn/end',seq:2,time:3,data:{reason:{kind:'completed'}}}];
 assert.equal(turnFacts(events,'input').publicText.text,'public');
 const withInstructions=[events[0],{type:'user/message',seq:0,time:1,data:{source:{kind:'agent-instructions'}}},...events.slice(1)];
 assert.equal(turnFacts(withInstructions,'input').publicText.text,'public');
 assert.equal(turnFacts(events,'other').consumed,false);
 const failed=turnFacts([...events,{type:'turn/end',seq:3,time:4,data:{reason:{kind:'error'}}}],'input');assert.equal(failed.failed,true);assert.equal(failed.publicText,undefined);
 const beforeRequest=turnFacts([{type:'agent/inbox/spliced',seq:0,time:1,data:{inserted:[{source:{kind:'clawmanager-team',inputId:'input'}}]}},{type:'turn/end',seq:1,time:2,data:{reason:{kind:'error'}}}],'input');
 assert.equal(beforeRequest.consumed,true);assert.equal(beforeRequest.failed,true);
 assert.equal(turnFacts(events.slice(0,2),'input').publicText,undefined,'running prose is not a final delivery');
});
