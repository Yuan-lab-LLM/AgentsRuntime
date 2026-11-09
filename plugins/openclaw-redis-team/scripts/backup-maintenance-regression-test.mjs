import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

if (!process.env.REDIS_TEAM_TEST_URL) throw new Error("disposable REDIS_TEAM_TEST_URL required");
const root = await fs.mkdtemp(path.join(os.tmpdir(), "backup-maintenance-"));
process.env.XDG_STATE_HOME = path.join(root, "state");
const source = (await fs.readFile(new URL("../dist/index.js", import.meta.url), "utf8"))
  .replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";', "const definePluginEntry = (entry) => entry;")
  .replace("const READ_BLOCK_MS = 15000;", "const READ_BLOCK_MS = 100;");
const {RedisClient, startConsumer} = await import(`data:text/javascript;base64,${Buffer.from(source+"\nexport {RedisClient,startConsumer};").toString("base64")}`);
const teamId = "backup-"+crypto.randomUUID();
const prefix = "claw:team:"+teamId;
const cfg = {enabled:true,redisUrl:process.env.REDIS_TEAM_TEST_URL,teamId,memberId:"developer",role:"developer",sharedDir:path.join(root,"shared"),consumerGroup:"backup-regression",inboxKey:prefix+":inbox:developer",eventsKey:prefix+":events",presenceKey:prefix+":presence",dlqKey:prefix+":dlq"};
const redis = new RedisClient(cfg.redisUrl);
let consumer;
const seen=[];
try {
  await fs.mkdir(cfg.sharedDir,{recursive:true});
  await redis.connect();
  for (const id of ["one","two"]) {
    await redis.command("XADD",cfg.inboxKey,"*","payload",JSON.stringify({v:4,protocolVersion:4,messageId:id,idempotencyKey:id,taskId:"task",rootTaskId:"task",teamId,from:"leader",to:"developer",intent:"member_result_confirmed",type:"notification",text:id,requiresCompletion:false}));
  }
  consumer = await startConsumer(cfg,async envelope=>{
    seen.push(envelope.messageId);
    if (seen.length===1) await redis.command("SET",prefix+":maintenance",JSON.stringify({enabled:true,rolloutId:"test-maintenance"}),"PX",10000);
  },null,{info(){},warn(){},error(message){throw new Error(message)}});
  const deadline=Date.now()+5000;
  while (!seen.length && Date.now()<deadline) await new Promise(resolve=>setTimeout(resolve,20));
  await new Promise(resolve=>setTimeout(resolve,300));
  assert.deepEqual(seen,["one"],"a maintenance change inside a delivered batch must block its remaining messages");
  await redis.command("DEL",prefix+":maintenance");
  const resumeDeadline=Date.now()+5000;
  while (seen.length<2 && Date.now()<resumeDeadline) await new Promise(resolve=>setTimeout(resolve,20));
  assert.deepEqual(seen,["one","two"],"pending message must resume without duplication");
  console.log("PASS: batch maintenance pause and pending resume");
} finally {
  if (consumer) await consumer.stop();
  const keys=await redis.command("KEYS",prefix+":*");
  if(keys.length) await redis.command("DEL",...keys);
  redis.close();
  await fs.rm(root,{recursive:true,force:true});
}
