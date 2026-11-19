import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
const directory=process.env.RUNTIME_DURABLE_TEST_STATE;
assert.ok(directory,'RUNTIME_DURABLE_TEST_STATE required; fixture state must be isolated');
process.env.XDG_STATE_HOME=directory;
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry = x => x;');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport { terminalJournal, RedisClient, startConsumer, createRuntime };').toString('base64')}`);
const cfg={fromEnv:false,enabled:true,teamId:'runtime-durable-openclaw'+(process.env.RUNTIME_DURABLE_TEST_SUFFIX||''),memberId:'worker',role:'worker',sharedDir:path.join(directory,'shared'),redisUrl:process.env.REDIS_TEAM_TEST_URL,consumerGroup:'durable',autoRun:true};
const redis=new p.RedisClient(cfg.redisUrl);await redis.connect();
assert.equal(await redis.command('GET','cm:test:runtime-durable:isolated'),'runtime-durable-20261118');
const inbox=`claw:team:${cfg.teamId}:inbox:worker`,events=`claw:team:${cfg.teamId}:events`;
const envelope={v:1,messageId:'durable-inbound-1',idempotencyKey:'durable-inbound-1',teamId:cfg.teamId,from:'leader',to:'worker',taskId:'root-durable-1',rootTaskId:'root-durable-1',assignmentId:'work-1',revision:1,text:'Execute exactly once'};
const callsFile=path.join(directory,'business-calls');
let consumer;const logs=[];const log={info:()=>{},warn:x=>logs.push(String(x)),error:x=>logs.push(String(x))};
const wait=async(fn)=>{for(let i=0;i<300;i++){if(await fn())return;await new Promise(r=>setTimeout(r,50));}throw Error('Timed out: '+logs.slice(-8).join('\n'));};
try {
 if(process.argv[2]==='prepare'){
  await fs.mkdir(directory,{recursive:true});await fs.writeFile(callsFile,'0');
  const runtime=p.createRuntime({config:{channels:{'redis-team':{accounts:{default:cfg}}}},logger:log});
  consumer=await p.startConsumer(cfg,async received=>{
   await fs.writeFile(callsFile,String(Number(await fs.readFile(callsFile,'utf8'))+1));
   await redis.command('CONFIG','SET','maxmemory','1');
   const outcome=await runtime.withActiveEnvelope(received,()=>runtime.completeTask({taskId:received.taskId,status:'succeeded',summary:'Actual completed work',resultMarkdown:'full real tool result survives OOM'}),cfg);
   assert.equal(outcome.completionPending,true);
  },async()=>{throw Error('must not convert storage failure into task failure')},log);
  await redis.command('XADD',inbox,'*','payload',JSON.stringify(envelope));
  await wait(async()=>!!(await p.terminalJournal(cfg).read(envelope))?.publications?.length);
  await consumer.stop();consumer=null;
  const row=await p.terminalJournal(cfg).read(envelope);
  assert.equal(row.publications[0].event.resultMarkdown,'full real tool result survives OOM');
  assert.equal(row.publications[0].streamId,'');
  assert.equal(Number(await fs.readFile(callsFile,'utf8')),1);
  assert.equal((await redis.command('XPENDING',inbox,cfg.consumerGroup))[0],1);
  await redis.command('CONFIG','SET','maxmemory','16777216');
  console.log(JSON.stringify({phase:'prepare',realNoevictionOOM:true,actualCompletionTool:true,resultDurable:true,pending:1,businessCalls:1}));
 }else{
  const before=await p.terminalJournal(cfg).read(envelope);const attempt=before.publications[0].event.attemptId;
  consumer=await p.startConsumer(cfg,async()=>{throw Error('business must not be re-executed after restart')},async()=>{throw Error('unexpected failure handler')},log);
  await wait(async()=>Number((await redis.command('XPENDING',inbox,cfg.consumerGroup))[0])===0);
  const row=await p.terminalJournal(cfg).read(envelope);assert.equal(row.publications[0].event.attemptId,attempt);
  const decode=row=>JSON.parse(row[1][row[1].indexOf('payload')+1]);
  const terminal=(await redis.command('XRANGE',events,'-','+')).map(decode).filter(e=>e.event==='completion_proposed');
  assert.equal(terminal.length,1);assert.equal(terminal[0].resultMarkdown,'full real tool result survives OOM');
  await redis.command('XADD',inbox,'*','payload',JSON.stringify(envelope));
  await new Promise(r=>setTimeout(r,250));
  assert.equal(Number(await fs.readFile(callsFile,'utf8')),1);
  assert.equal((await redis.command('XRANGE',events,'-','+')).map(decode).filter(e=>e.event==='completion_proposed').length,1);
  const lost={messageId:'lost-reply',taskId:'lost-reply'};
  const journal=p.terminalJournal(cfg);await journal.admit(lost);await journal.started(lost);
  await journal.stage(lost,{event:'completion_proposed',eventId:'lost-reply',completionId:'lost-reply',attemptId:'lost-reply',resultMarkdown:'completed before lost reply'});
  await assert.rejects(journal.flush({async command(...args){const result=await redis.command(...args);if(args[0]==='EVAL')throw Error('lost response after Redis committed');return result;}},lost),/lost response/);
  await p.terminalJournal(cfg).flush(redis,lost);
  assert.equal((await redis.command('XRANGE',events,'-','+')).map(decode).filter(e=>e.eventId==='lost-reply').length,1);
  console.log(JSON.stringify({phase:'recover',redisAOFRestart:true,processRestart:true,pendingRecovered:true,stableAttempt:true,duplicateSuppressed:true,lostPublishResponse:true,businessCalls:1}));
 }
}finally{await consumer?.stop();redis.close();}
