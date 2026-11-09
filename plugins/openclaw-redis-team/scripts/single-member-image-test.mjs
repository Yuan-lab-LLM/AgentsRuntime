// Runs the installed plugin and real OpenClaw SDK in a disposable image against
// a real isolated ClawManager/Redis. Deterministic tool calls, NOT a model test.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { pathToFileURL } from 'node:url';

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'single-member-image-'));
await fs.mkdir(path.join(root,'node_modules'));
await fs.symlink('/usr/local/lib/node_modules/openclaw',path.join(root,'node_modules/openclaw'),'dir');
const installed='/defaults/.openclaw/extensions/redis-team/dist/index.js';
await fs.writeFile(path.join(root,'package.json'),JSON.stringify({type:'module'}));
await fs.writeFile(path.join(root,'entry.js'),await fs.readFile(installed,'utf8')+'\nexport { createRuntime, RedisClient, normalizeEnvelope, isSingleMemberEnvelope };\n');
const plugin=await import(pathToFileURL(path.join(root,'entry.js')).href);
process.env.XDG_STATE_HOME=path.join(root,'state');
const teamId=process.env.TEAM_FOLLOWUP_TEAM_ID || '1', memberId=process.env.TEAM_FOLLOWUP_WORKER || 'open-worker';
const cfg={fromEnv:false,enabled:true,redisUrl:process.env.TEAM_FOLLOWUP_REDIS_URL,teamId,memberId,role:'worker',sharedDir:process.env.TEAM_FOLLOWUP_SHARED};
const runtime=plugin.createRuntime({config:{channels:{'redis-team':{accounts:{default:cfg}}}},logger:{info(){},warn(){},error(){}}});
const redis=new plugin.RedisClient(cfg.redisUrl);
try {
  await redis.connect();
  const rows=await redis.command('XREVRANGE',`claw:team:${teamId}:inbox:${memberId}`,'+','-','COUNT','1');
  const fields=rows[0][1]; const raw=JSON.parse(fields[fields.indexOf('payload')+1]);
  const envelope=plugin.normalizeEnvelope(raw);
  assert.equal(plugin.isSingleMemberEnvelope(cfg,envelope),true);
  assert.equal(envelope.assignmentId,'single-member');
  assert.ok(envelope.text.includes('verified sources'));
  assert.ok(envelope.followupContext.text.includes('Collected source A and B'));
  assert.equal(raw.conversationId,undefined,'backend must not override native session routing');
  assert.equal(envelope.conversationId,raw.taskId,'existing Runtime normalization unchanged');
  assert.equal(plugin.isSingleMemberEnvelope(cfg,{...envelope,from:'leader'}),false);
  assert.equal(plugin.isSingleMemberEnvelope(cfg,{...envelope,rootTaskId:'team-2-task-99'}),false);
  const before=Number(await redis.command('XLEN',`claw:team:${teamId}:inbox:leader`));
  const old=envelope.followupContext.artifactRefs[0];
  const oldPath=path.join(cfg.sharedDir,old.replace(/^\/team\//,''));
  const original=await fs.readFile(oldPath,'utf8');
  const output=`/team/artifacts/${envelope.rootTaskId}/members/${memberId}/single-member/revised.md`;
  let receipt;
  await runtime.withActiveEnvelope(envelope,async()=>{
    await runtime.updateProgress({status:'running',progress:20,summary:'开始修改报告'});
    const read=await runtime.artifactRead({path:old});
    assert.ok(JSON.stringify(read).includes('original citations'));
    await runtime.artifactWrite({path:output,content:'# 修改后报告\noriginal citations\n'});
    const send=await runtime.send({to:'leader',text:'Please summarize'});
    assert.equal(send.code,'single_member_no_delegation');
    receipt=await runtime.completeTask({status:'succeeded',summary:'报告修改完成',resultMarkdown:'已保留原始引用并生成新报告。',artifactRefs:[output]});
  });
  assert.equal(receipt.completion?.decision,'accepted',JSON.stringify(receipt));
  assert.equal(await fs.readFile(oldPath,'utf8'),original);
  assert.equal(Number(await redis.command('XLEN',`claw:team:${teamId}:inbox:leader`)),before,'single execution does not wake Leader');
  console.log(JSON.stringify({runtime:'openclaw',rootTaskId:envelope.rootTaskId,decision:receipt.completion.decision,output,nativeSdk:true,modelCalls:0}));
} finally {redis.close();await fs.rm(root,{recursive:true,force:true});}
