// Installed SDK + actual inbox consumer/delivery callback + ClawManager outbox.
// Only model execution is deterministic; no paid model or production resources.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {pathToFileURL} from 'node:url';
const tmp=await fs.mkdtemp(path.join(os.tmpdir(),'completion-recovery-'));
await fs.mkdir(path.join(tmp,'node_modules'));
await fs.symlink('/usr/local/lib/node_modules/openclaw',path.join(tmp,'node_modules/openclaw'),'dir');
await fs.writeFile(path.join(tmp,'package.json'),'{"type":"module"}');
await fs.writeFile(path.join(tmp,'entry.js'),await fs.readFile('/defaults/.openclaw/extensions/redis-team/dist/index.js','utf8')+'\nexport { RedisClient };\n');
const plugin=await import(pathToFileURL(path.join(tmp,'entry.js')).href);
process.env.XDG_STATE_HOME=path.join(tmp,'state');
const cfg={fromEnv:false,enabled:true,autoRun:true,redisUrl:process.env.TEAM_FOLLOWUP_REDIS_URL,teamId:'1',memberId:'open-worker',role:'worker',sharedDir:process.env.TEAM_FOLLOWUP_SHARED,consumerGroup:'completion-recovery-image'};
const config={channels:{'redis-team':{accounts:{default:cfg}}}};
const tools=new Map();let channel,receipt,firstMessage,dispatches=0;
const logs=[];const log={info(m){logs.push(String(m));},warn(m){logs.push(String(m));},error(m){logs.push(String(m));}};
const answer='本轮完整答案：资讯来自十二个已核对的来源。保留原始引用，不需要额外文件。';
plugin.default.register({config,logger:{info(){},warn(){},error(){}},registerCli(){},registerService(){},on(){},registerTool(factory,meta){tools.set(meta.name,factory);},registerChannel(c){channel=c.plugin;}});
const redis=new plugin.RedisClient(cfg.redisUrl),abort=new AbortController();let run;
const wait=async(check)=>{const until=Date.now()+65000;while(Date.now()<until){if(await check())return;await new Promise(r=>setTimeout(r,100));}throw Error('actual consumer recovery timed out');};
try {
  await redis.connect();
  const rows=await redis.command('XREVRANGE','claw:team:1:inbox:open-worker','+','-','COUNT','100');
  const decode=row=>JSON.parse(row[1][row[1].indexOf('payload')+1]);
  const index=rows.findIndex(row=>decode(row).rootTaskId===process.env.TEAM_RECOVERY_ROOT && decode(row).intent==='run_task');
  assert.ok(index>=0,'find the exact HTTP-created task, never the latest asynchronous notification');
  const initial=decode(rows[index]);
  firstMessage=initial.messageId;
  // Earlier fixture cases exercised tools directly and left their historical
  // rejection notifications unread. This consumer owns only the new case.
  await redis.command('XGROUP','CREATE','claw:team:1:inbox:open-worker',cfg.consumerGroup,rows[index+1]?.[0] || '0-0','MKSTREAM');
  const context={accountId:'default',cfg:config,abortSignal:abort.signal,setStatus(){},log,channelRuntime:{
    routing:{resolveAgentRoute(){return {agentId:'main',accountId:'default',sessionKey:'agent:main:redis-team:group:1'};}},
    inbound:{async buildContext(p){return p;},async run({adapter}){
      const plan=adapter.resolveTurn();
      if(plan.ctxPayload.extra.RedisTeamTaskId!==initial.rootTaskId) {
        logs.push('Historical fixture notification: '+plan.ctxPayload.extra.RedisTeamTaskId);
        return {};
      }
      const body=plan.ctxPayload.message.bodyForAgent;
      if (!body.includes('[ROOT_TASK_RECOVERY]')) {
        dispatches++;
        await plan.delivery.deliverWithProviderMessageSending({text:answer});
      } else {
        assert.match(body,/本轮完整答案/,'actual backend recovery must include captured answer');
        assert.match(body,/text-only answer/);
        const tool=tools.get('team_complete_task')({messageChannel:'redis-team',sessionKey:'agent:main:redis-team:group:1',nativeChannelId:'1'});
        const result=await tool.execute('deterministic-completion',{status:'succeeded',summary:'资讯来源说明',resultMarkdown:answer});
        receipt=JSON.parse(result.content[0].text);
      }
      return {};
    }}
  }};
  run=channel.gateway.startAccount(context);
  await wait(()=>receipt);
  assert.equal(receipt.completion?.decision,'accepted',JSON.stringify(receipt));
  assert.equal(dispatches,1,'must reuse answer instead of redispatching the original task');
  const all=await redis.command('XRANGE','claw:team:1:events','-','+');
  const events=all.map(r=>JSON.parse(r[1][r[1].indexOf('payload')+1])).filter(p=>p.rootTaskId===initial.rootTaskId);
  const ended=events.find(p=>p.eventKind==='turn_finished_without_completion');
  assert.equal(ended?.resultMarkdown,answer);
  assert.equal(ended?.hadAssistantNarrative,true);
  assert.equal(ended?.visibleToChat,false);
  assert.equal(events.filter(p=>p.event==='completion_proposed').length,1);
  assert.equal(events.some(p=>p.eventKind==='agent_narrative'),false);
  console.log(JSON.stringify({runtime:'openclaw',root:initial.rootTaskId,callbackCaptured:true,immediateRecovery:true,decision:'accepted',modelCalls:0}));
} catch(error) {
  console.error(JSON.stringify({diagnostic:'isolated callback recovery',firstMessage,dispatches,logs:logs.slice(-16),events:await redis.command('XREVRANGE','claw:team:1:events','+','-','COUNT','6')}));
  throw error;
} finally {abort.abort();await run?.catch(()=>{});redis.close();await fs.rm(tmp,{recursive:true,force:true});}
