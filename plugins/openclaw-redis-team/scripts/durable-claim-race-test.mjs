import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {setTimeout as sleep} from 'node:timers/promises';
const root=await fs.mkdtemp(path.join(os.tmpdir(),'durable-claim-race-'));process.env.XDG_STATE_HOME=root;
let source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry=x=>x;').replace('const READ_BLOCK_MS = 15000;','const READ_BLOCK_MS = 100;');
// Negative control restores the reviewed unconditional consumer transition.
if(process.env.RUNTIME_CLAIM_NEGATIVE_CONTROL==='1')source=source.replace('if (!await journal.started(envelope)) continue;','await journal.uncertain(envelope);');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport {terminalJournal,RedisClient,startConsumer};\n//# sourceURL=claim-race-protocol.mjs').toString('base64')}`);
const redis=new p.RedisClient(process.env.REDIS_TEAM_TEST_URL);await redis.connect();
assert.equal(await redis.command('GET','cm:test:runtime-durable:isolated'),'runtime-durable-20261118');
const originalOpen=fs.open,originalCommand=p.RedisClient.prototype.command;
const wait=async(fn)=>{for(let i=0;i<150;i++){if(await fn())return;await sleep(20)}throw Error('race condition timed out')};
const log={info(){},warn(){},error(){}};
let consumer;
try{
 for(const ordering of ['context-first','serial-first','context-uncertain']){
  const contextFirst=ordering!=='serial-first',uncertain=ordering==='context-uncertain';
  const cfg={enabled:true,redisUrl:process.env.REDIS_TEAM_TEST_URL,teamId:'claim-'+ordering+'-'+crypto.randomUUID(),memberId:'worker',sharedDir:path.join(root,ordering),consumerGroup:'claim-race'};
  const inbox=`claw:team:${cfg.teamId}:inbox:worker`;
  const e={teamId:cfg.teamId,to:'worker',from:'clawmanager-monitor',taskId:'root',rootTaskId:'root',assignmentId:'work',workId:'work',revision:1,messageId:'notice',idempotencyKey:'notice',requiresCompletion:false,businessDeliveryKind:'context',businessMutation:false,text:'Continue original work'};
  const j=p.terminalJournal(cfg);await j.admit(e);await j.context(e,'queued');
  let serial=0,context=0,scheduled=false,claimPromise;
  fs.open=async function(file,flags,...args){
   const h=await originalOpen.call(this,file,flags,...args);
   const admissionRead=String(new Error().stack).includes('admit (claim-race-protocol.mjs:');
   if(!scheduled&&String(file).includes('terminal-delivery')&&flags===(contextFirst?'r':'r+')&&(!contextFirst||admissionRead)){
    scheduled=true;
    // Queue the actual context claim while the consumer holds the journal
    // admission read (first ordering) or its execution write (second).
    claimPromise=j.context(e,'submitting',{marker:'race-marker',generation:1}).then(async claim=>{
     if(claim){context++;if(!uncertain)await j.context(e,'submitted',{nativeMessageId:'native-once'});}
    });
   }
   return h;
  };
  // Lose transport ACK after the winner's durable handoff, then restart.
  p.RedisClient.prototype.command=async function(...args){if(args[0]==='XACK'&&args[1]===inbox)throw Error('lost final inbox ACK');return originalCommand.apply(this,args)};
  consumer=await p.startConsumer(cfg,async()=>{serial++},async()=>{throw Error('claim contention is not unexecuted failure')},log);
  await redis.command('XADD',inbox,'*','payload',JSON.stringify(e));
  await wait(()=>scheduled);await claimPromise;await wait(()=>serial+context>=1);await sleep(200);
  assert.equal(serial+context,1,`one execution owner required: ${ordering}`);
  assert.equal(context,contextFirst?1:0);assert.equal(serial,contextFirst?0:1);
  await consumer.stop();consumer=null;fs.open=originalOpen;p.RedisClient.prototype.command=originalCommand;
  assert.equal((await redis.command('XPENDING',inbox,cfg.consumerGroup))[0],1);
  // Marker loss as well as ACK loss cannot reauthorize the native action.
  const keys=await redis.command('KEYS',`claw:team:${cfg.teamId}:processed:*`);for(const key of keys)await redis.command('DEL',key);
  consumer=await p.startConsumer(cfg,async()=>{serial++},async()=>{throw Error('unexpected failure')},log);
  if(!uncertain)await wait(async()=>(await redis.command('XPENDING',inbox,cfg.consumerGroup))[0]===0);
  await sleep(250);
  assert.equal(serial+context,1);assert.equal((await j.read(e)).state,uncertain?'started':'accepted');
  assert.equal((await redis.command('XPENDING',inbox,cfg.consumerGroup))[0],uncertain?1:0);
  await consumer.stop();consumer=null;
  console.log(JSON.stringify({ordering,queuedActualContextClaim:true,serialDispatches:serial,nativeContextSubmissions:context,lostACK:true,lostProcessedMarker:true,restartExecutions:0,uncertainStillPending:uncertain}));
 }
}finally{fs.open=originalOpen;p.RedisClient.prototype.command=originalCommand;await consumer?.stop();redis.close();await fs.rm(root,{recursive:true,force:true});}
