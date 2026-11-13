// Real pinned native Web + real Redis; only the model is a local fixture.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { setTimeout as sleep } from 'node:timers/promises';
import * as protocol from '/opt/clawmanager-dsh/adapter/deepseek-harness/team/protocol.mjs';

const root=await fs.mkdtemp(path.join(os.tmpdir(),'dsh-team-native-')),shared=path.join(root,'shared');
await fs.mkdir(shared);await fs.mkdir(path.join(root,'workspace'));
await fs.writeFile(path.join(root,'workspace','AGENTS.md'),'Use the registered Team tools for the assignment. Preserve public results.\n');
const teamId=String(Date.now());let calls=0,raw='',activeModelRequest=false,contextIncluded=false;
const contextFixture=process.env.TEAM_CONTEXT_FIXTURE==='1';
const model=createServer(async(req,res)=>{
 const chunks=[];for await(const c of req)chunks.push(c);const body=JSON.parse(Buffer.concat(chunks));
 assert.equal(req.headers.authorization,'Bearer fixture');calls++;
 const tools=body.tools||[];
 if(tools.length){
  const complete=tools.find(t=>t.function.name==='team_complete_task')?.function.parameters;
  assert.deepEqual(complete?.required,['status','summary']);assert.ok(complete.properties.resultMarkdown&&complete.properties.validationTargetAssignmentId&&complete.properties.artifactRefs);
 }
 if(contextFixture && tools.length){
  if(!activeModelRequest){activeModelRequest=true;await sleep(8500);}
  if(JSON.stringify(body.messages).includes('Native reminder fixture marker'))contextIncluded=true;
 }
 const results=body.messages.filter(m=>m.role==='tool');
 let delta;
 if(!tools.length)delta={role:'assistant',content:'Fixture title'};
 else if(results.length===0)delta={role:'assistant',tool_calls:[{index:0,id:'artifact-native-call',type:'function',function:{name:'team_artifact_write',arguments:JSON.stringify({path:'report.md',content:'# Native DSH fixture\nActual artifact.'})}}]};
 else if(results.length===1)delta={role:'assistant',tool_calls:[{index:0,id:'complete-native-call',type:'function',function:{name:'team_complete_task',arguments:JSON.stringify({status:'done',summary:'native fixture completed'})}}]};
 else delta={role:'assistant',content:'DSH native public final answer'};
 res.writeHead(200,{'Content-Type':'text/event-stream'});
 for(const [value,finish]of[[delta,null],[{},delta.tool_calls?'tool_calls':'stop']])res.write('data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:1,model:body.model,choices:[{index:0,delta:value,finish_reason:finish}]})+'\n\n');
 res.end('data: [DONE]\n\n');
});
await new Promise(resolve=>model.listen(0,'127.0.0.1',resolve));
Object.assign(process.env,{CLAWMANAGER_TEAM_ENABLED:'true',CLAWMANAGER_TEAM_ID:teamId,CLAWMANAGER_TEAM_MEMBER_ID:'worker',CLAWMANAGER_TEAM_ROLE:'worker',CLAWMANAGER_TEAM_REDIS_URL:process.env.REDIS_TEAM_TEST_URL,CLAWMANAGER_TEAM_SHARED_DIR:shared,CLAWMANAGER_GATEWAY_GENERATION:'2',XDG_STATE_HOME:path.join(root,'state')});
delete process.env.CLAWMANAGER_DEFAULT_PROJECT_PATH;delete process.env.CLAWMANAGER_PROJECT_PATH;
await fs.writeFile(path.join(shared,'team.json'),JSON.stringify({teamId,members:[{memberId:'leader',role:'leader',isLeader:true},{memberId:'worker',role:'worker'}]}));
const env={...process.env,HOME:root,DSH_HOME:path.join(root,'.dsh'),NARB_DISABLE_NATIVE_CACHE:'1',CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION:'dsh-team-v1',CLAWMANAGER_LLM_API_KEY:'fixture',CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON:JSON.stringify({default_provider:'clawmanager',default_model:'auto',providers:[{provider_id:'clawmanager',model_ids:['auto']}],api_key_env:'CLAWMANAGER_LLM_API_KEY',base_url:`http://127.0.0.1:${model.address().port}/v1`})};
let child;
function startNative(){child=spawn('dsh',['web','--patch','/usr/local/share/deepseek-harness/managed.cordis.patch.yml','--patch','/usr/local/share/deepseek-harness/team.cordis.patch.yml','--host','127.0.0.1','--port','0','--no-open'],{env,cwd:path.join(root,'workspace'),stdio:['ignore','pipe','pipe']});child.stdout.on('data',b=>{raw+=b;});child.stderr.on('data',b=>{raw+=b;});}
async function stopNative(){if(child.exitCode!==null)return;const ended=new Promise(resolve=>child.once('exit',resolve));child.kill('SIGTERM');await Promise.race([ended,sleep(12000,undefined,{ref:false}).then(()=>{child.kill('SIGKILL');throw Error('Native stop timeout');})]);}
startNative();
const redis=new protocol.RedisClient(process.env.REDIS_TEAM_TEST_URL);
const wait=async fn=>{const until=Date.now()+80000;while(Date.now()<until){if(child.exitCode!==null||raw.includes('did not activate'))throw Error('Native plugin startup failed');const result=await fn();if(result)return result;await sleep(200);}throw Error('Native Team condition timed out');};
const payloads=rows=>(rows||[]).map(row=>JSON.parse(row[1][row[1].indexOf('payload')+1]));
try{
 await redis.connect();const prefix=`claw:team:${teamId}`;
 await wait(async()=>await redis.command('HGET',prefix+':presence','worker'));
 const envelope={v:4,protocolVersion:4,teamId,taskId:`team-${teamId}-task-1`,rootTaskId:`team-${teamId}-task-1`,messageId:'dsh-native-1',idempotencyKey:'dsh-native-1',assignmentId:'write',workId:'write',revision:1,from:'leader',to:'worker',type:'assignment',text:'Write report.md and formally submit. Then give the public final answer.',createdAt:new Date().toISOString()};
 await redis.command('XADD',prefix+':inbox:worker','*','payload',JSON.stringify(envelope));
 if(contextFixture){
  await wait(()=>activeModelRequest);
  await redis.command('XADD',prefix+':inbox:worker','*','payload',JSON.stringify({...envelope,messageId:'native-context-1',idempotencyKey:'native-context-1',from:'clawmanager-monitor',businessDeliveryKind:'context',businessMutation:false,requiresCompletion:false,text:'Native reminder fixture marker. Continue this original assignment without restarting.'}));
 }
 // Controlled acknowledgement stands in for CM only in this isolated contract
 // fixture. Final release acceptance below uses the real CM completion gate.
 let acknowledged=false;
 const events=await wait(async()=>{const v=payloads(await redis.command('XRANGE',prefix+':events','-','+'));
  const proposal=v.find(x=>x.event==='completion_proposed');
  if(proposal&&!acknowledged){assert.notEqual(proposal.rootTaskTerminal,true);await redis.command('SET',prefix+':completion-ack:'+proposal.completionId+':'+proposal.attemptId,JSON.stringify({decision:'accepted'}));acknowledged=true;}
  return v.some(x=>x.eventKind==='delivery_supplement')&&v;});
 assert.equal(events.filter(x=>x.event==='completion_proposed').length,1);
 if(contextFixture){assert.ok(contextIncluded,'Reminder must reach an actual native model request');assert.ok(events.some(x=>x.eventKind==='runtime_context_delivery'),'Context receipt required');}
 assert.ok(events.some(x=>x.eventKind==='delivery_supplement'&&x.text==='DSH native public final answer'));
 assert.match(await fs.readFile(path.join(shared,`artifacts/${envelope.taskId}/members/worker/write/report.md`),'utf8'),/Actual artifact/);
 const before=calls;await redis.command('XADD',prefix+':inbox:worker','*','payload',JSON.stringify(envelope));await sleep(800);assert.equal(calls,before);
 const journalDir=path.join(root,'state','teams',teamId,'worker','dsh-inputs');const journals=await fs.readdir(journalDir);assert.equal(journals.length,1);const journal=await fs.readFile(path.join(journalDir,journals[0]),'utf8');
 const oldSeen=JSON.parse(await redis.command('HGET',prefix+':presence','worker')).lastSeenAt;
 await stopNative();raw='';startNative();
 await wait(async()=>{const value=await redis.command('HGET',prefix+':presence','worker');return value&&JSON.parse(value).lastSeenAt!==oldSeen;});
 await redis.command('XADD',prefix+':inbox:worker','*','payload',JSON.stringify(envelope));await sleep(800);assert.equal(calls,before);assert.equal(await fs.readFile(path.join(journalDir,journals[0]),'utf8'),journal);
 const match=await wait(async()=>raw.match(/http:\/\/127\.0\.0\.1:\d+\/\?token=[A-Za-z0-9_%.-]+/));
 const login=await fetch(match[0],{redirect:'manual'}),origin=new URL(match[0]).origin,cookie=login.headers.getSetCookie().map(x=>x.split(';')[0]).join('; ');
 const rpc=async(method,args)=>{const r=await fetch(origin+'/api/'+method,{method:'POST',headers:{Cookie:cookie,Origin:origin,'Content-Type':'application/json'},body:JSON.stringify({type:'client-request',rpcId:'fixture-'+Date.now(),method,payload:{args:method==='session/list'?{_request:args}:{request:args}}})});return(await r.json()).result.value;};
 const session=(await rpc('session/list',{})).items[0];const projection=await rpc('session/projections',{sessionId:session.sessionId});
 const page=await rpc('session/page',{address:{kind:'session',sessionId:session.sessionId},throughSeq:projection.asOfSeq});
 assert.ok(page.records.some(x=>x.event?.type==='user/message'&&x.event.data.source?.kind==='agent-instructions'),'the real native AGENTS.md context must be exercised');
 console.log(JSON.stringify({native:'0.2.1-rc.3',presence:true,full_tool_schema:true,minimal_receipt:true,active_context_request:contextFixture?contextIncluded:undefined,artifact:true,public_supplement:true,native_agent_instructions:true,duplicate_model_execution:false,process_restart_retains_input:true,confirmed_stop:true}));
}catch(e){
 console.error('fixture model calls:',calls);
 const match=raw.match(/http:\/\/127\.0\.0\.1:\d+\/\?token=[A-Za-z0-9_%.-]+/);
 if(match)try{
  const login=await fetch(match[0],{redirect:'manual'});const origin=new URL(match[0]).origin,cookie=login.headers.getSetCookie().map(x=>x.split(';')[0]).join('; ');
  const rpc=async(method,args)=>{const r=await fetch(origin+'/api/'+method,{method:'POST',headers:{Cookie:cookie,Origin:origin,'Content-Type':'application/json'},body:JSON.stringify({type:'client-request',rpcId:'diagnostic-'+Date.now(),method,payload:{args:method==='session/list'?{_request:args}:{request:args}}})});return(await r.json()).result;};
  const sessions=await rpc('session/list',{});console.error('native sessions:',JSON.stringify(sessions));
  for(const session of sessions.value?.items||[]){const projection=await rpc('session/projections',{sessionId:session.sessionId});const page=await rpc('session/page',{address:{kind:'session',sessionId:session.sessionId},throughSeq:projection.value.asOfSeq});console.error('native event facts:',JSON.stringify(page.value?.records.map(x=>({type:x.event?.type,reason:x.event?.data?.reason,error:x.event?.data?.error,context:x.event?.type==='agent/inbox/spliced'?x.event.data.inserted?.map(m=>({source:m.source,id:m.id,content:m.content})):undefined}))))}
 }catch{}
 console.error(raw.replace(/\?token=\S+/g,'?token=[redacted]').slice(-10000));throw e;
}
finally{await stopNative();redis.close();model.closeAllConnections();await new Promise(resolve=>model.close(resolve));await fs.rm(root,{recursive:true,force:true});}
