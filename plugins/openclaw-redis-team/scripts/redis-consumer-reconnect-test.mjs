import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {setTimeout as sleep} from 'node:timers/promises';
if(!process.env.REDIS_TEAM_TEST_URL)throw Error('Disposable Redis required');
const root=await fs.mkdtemp(path.join(os.tmpdir(),'cm-redis-reconnect-'));
process.env.XDG_STATE_HOME=path.join(root,'state');
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry=entry=>entry;').replace('const READ_BLOCK_MS = 15000;','const READ_BLOCK_MS = 100;');
const {RedisClient,startConsumer}=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport {RedisClient,startConsumer};').toString('base64')}`);
const teamId='reconnect-'+crypto.randomUUID(),prefix='claw:team:'+teamId;
const cfg={enabled:true,redisUrl:process.env.REDIS_TEAM_TEST_URL,teamId,memberId:'worker',role:'worker',sharedDir:path.join(root,'shared'),consumerGroup:'reconnect-test',inboxKey:prefix+':inbox:worker',eventsKey:prefix+':events',presenceKey:prefix+':presence',dlqKey:prefix+':dlq'};
const r=new RedisClient(cfg.redisUrl);await r.connect();let consumer;const seen=[];const errors=[];
const wait=async f=>{for(let n=0;n<300;n++){if(await f())return;await sleep(40)}throw Error('Bounded reconnect failed')};
const send=id=>r.command('XADD',cfg.inboxKey,'*','payload',JSON.stringify({v:4,protocolVersion:4,teamId,to:'worker',from:'leader',taskId:'task',rootTaskId:'task',workId:id,assignmentId:id,revision:1,messageId:id,idempotencyKey:id,text:'Existing work',requiresCompletion:true}));
try{
 consumer=await startConsumer(cfg,async x=>seen.push(x.messageId),null,{info(){},warn(){},error(e){errors.push(String(e))}});
 await send('first');await wait(()=>seen.length===1);
 let candidates=[];
 await wait(async()=>{candidates=String(await r.command('CLIENT','LIST')).split('\n').filter(x=>x.includes('cmd=xreadgroup'));return candidates.length===1});
 const id=candidates[0].match(/(?:^| )id=(\d+)/)[1];await r.command('CLIENT','KILL','ID',id);await send('second');await wait(()=>seen.length===2);
 assert.deepEqual(seen,['first','second'],'transport recovery must not repeat business execution');
 await consumer.stop();consumer=null;
 assert(errors.some(x=>x.includes('connection closed')),'did not exercise actual disconnect');
 console.log('PASS: real Redis expires consumer connection, serial consumption reconnects, next message arrives once, stop joins');
}finally{await consumer?.stop();r.close();await fs.rm(root,{recursive:true,force:true})}
