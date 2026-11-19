import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {setTimeout as sleep} from 'node:timers/promises';

if(!process.env.REDIS_TEAM_TEST_URL)throw Error('Disposable REDIS_TEAM_TEST_URL required');
const root=await fs.mkdtemp(path.join(os.tmpdir(),'team-active-context-'));
process.env.XDG_STATE_HOME=path.join(root,'state');
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry=entry=>entry;').replace('const READ_BLOCK_MS = 15000;','const READ_BLOCK_MS = 100;');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport {RedisClient,startConsumer,startActiveContextWatcher,activeContextMatches};').toString('base64')}`);
const teamId='context-'+crypto.randomUUID(),prefix='claw:team:'+teamId;
const cfg={enabled:true,redisUrl:process.env.REDIS_TEAM_TEST_URL,teamId,memberId:'worker',role:'worker',sharedDir:path.join(root,'shared'),consumerGroup:'context-test',inboxKey:prefix+':inbox:worker',eventsKey:prefix+':events',presenceKey:prefix+':presence',dlqKey:prefix+':dlq'};
const business={v:4,protocolVersion:4,teamId,to:'worker',from:'leader',taskId:'task',rootTaskId:'task',workId:'work',assignmentId:'work',revision:1,messageId:'business',idempotencyKey:'business',text:'Original work',requiresCompletion:true};
const active={envelope:business,generation:3};
const notice={...business,from:'clawmanager-monitor',requiresCompletion:false,businessDeliveryKind:'context',businessMutation:false,messageId:'notice',idempotencyKey:'notice',text:'Continue the original work'};
assert(p.activeContextMatches(cfg,active,notice));
for(const change of [{from:'peer'},{rootTaskId:'other'},{workId:'other'},{revision:2},{generation:2},{businessMutation:true}])assert(!p.activeContextMatches(cfg,active,{...notice,...change}),JSON.stringify(change));
const redis=new p.RedisClient(cfg.redisUrl);await redis.connect();
let release,businessCalls=0,held=null,watcher,consumer;const delivered=[];
const send=n=>redis.command('XADD',cfg.inboxKey,'*','payload',JSON.stringify(n));
const wait=async predicate=>{for(let i=0;i<100;i++){if(await predicate())return;await sleep(30)}throw Error('Bounded observation expired')};
try{
 consumer=await p.startConsumer(cfg,async n=>{if(n.requiresCompletion!==false){businessCalls++;held=active;await new Promise(r=>release=r);held=null}},null,{info(){},warn(){},error(e){throw Error(e)}});
 await send(business);await wait(()=>!!release);
 watcher=await p.startActiveContextWatcher(cfg,{getActive:()=>held,intervalMs:100,deliver:async(n,text)=>{delivered.push({id:n.messageId,text});return{state:'submitted',nativeMessageId:'native-notice'}}},{warn(){}});
 await send(notice);await wait(()=>delivered.length===1);
 assert.equal(businessCalls,1,'observer must not dispatch another execution');assert.match(delivered[0].text,/\[team-context:/);
 await watcher.observePreparedRequest({messages:[{content:delivered[0].text}]},{nativeSessionId:'owned-native'});
 await send(notice);await send({...notice,messageId:'stale',idempotencyKey:'stale',revision:2});await sleep(350);
 assert.equal(delivered.length,1,'same reminder must not be injected twice');
 const events=await redis.command('XRANGE',cfg.eventsKey,'-','+');assert(JSON.stringify(events).includes('request_included'));
 held=null;assert.equal(await watcher.confirm('notice'),false,'late request cannot confirm another execution');
 release();await sleep(250);assert.equal(businessCalls,1,'serial consumer must skip the accepted reminder');
 console.log('PASS: real Redis busy delivery, native-request confirmation, identity/revision/generation fences, dedup and serial business admission');
}finally{release?.();await watcher?.stop();await consumer?.stop();redis.close();await fs.rm(root,{recursive:true,force:true})}
