import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
const root=process.env.RUNTIME_ENOSPC_ROOT;
assert.ok(root?.includes('cm-runtime-enospc'),'explicit dedicated tmpfs root required');
process.env.XDG_STATE_HOME=path.join(root,'node');
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry = x => x;');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport {terminalJournal,createRuntime,writeActiveAssignmentEnvelope};\n//# sourceURL=enospc-protocol.mjs').toString('base64')}`);
const cfg={fromEnv:false,enabled:true,redisUrl:process.env.REDIS_TEAM_TEST_URL,teamId:'enospc-node',memberId:'worker',role:'worker',sharedDir:path.join(root,'shared'),consumerGroup:'enospc'};
const e={messageId:'accepted-native-work',idempotencyKey:'accepted-native-work',taskId:'root-enospc',rootTaskId:'root-enospc',assignmentId:'work',revision:1,text:'x'.repeat(64000)};
const j=p.terminalJournal(cfg);
if(process.argv[2]?.startsWith('crash-')){
 const original=fs.open;
 fs.open=async(...args)=>{
  const h=await original(...args);
  if(String(args[0]).includes('terminal-delivery')&&args[1]==='r+'){
   let synced=0;
   const write=h.write.bind(h),sync=h.sync.bind(h);
   h.write=async(buffer,...rest)=>{if(process.argv[2]==='crash-before'&&Buffer.isBuffer(buffer)&&buffer.subarray(0,5).toString()==='CMJ3 ')process.exit(88);return write(buffer,...rest)};
   h.sync=async()=>{await sync();if(++synced===2&&process.argv[2]==='crash-after')process.exit(89)};
  }
  return h;
 };
 await j.stage(e,{event:'reply',eventId:process.argv[2],text:'post-commit retained outcome'});
 throw Error('crash hook did not execute');
}
let filler;
try{
 await j.admit(e);await j.started(e);await p.writeActiveAssignmentEnvelope(cfg,e);
 const runtime=p.createRuntime({config:{channels:{'redis-team':{accounts:{default:cfg}}}},logger:{warn(){},info(){},error(){}}});
 const completion='C'.repeat(1024*1024-16384);
 await runtime.withActiveEnvelope(e,async()=>{
  filler=await fs.open(path.join(root,'node-fill'),'w');
  const block=Buffer.alloc(1024*1024,120);
  try{for(;;)await filler.write(block)}catch(error){assert.equal(error.code,'ENOSPC')}
  await filler.sync();await filler.close();filler=null;
  assert.equal((await fs.statfs(root)).bavail,0,'real filesystem must have zero available blocks');
  const outcome=await runtime.completeTask({taskId:e.taskId,status:'succeeded',summary:'Completed external action',resultMarkdown:completion});
  assert.equal(outcome.completion?.durable,true,'the result is durably pending artifact materialization');
 },cfg);
 const publicText='P'.repeat(4*1024*1024-16384);
 await j.stage(e,{event:'reply',eventKind:'delivery_supplement',nativePublicDelivery:true,eventId:'large-public',text:publicText});
 const row=await p.terminalJournal(cfg).read(e);
 assert.equal(row.publications[0].event.resultMarkdown,completion);
 assert.equal(row.publications[1].event.text,publicText);
 assert.equal(row.publications[1].materializationPending,true);
 const before=spawnSync(process.execPath,[process.argv[1],'crash-before'],{env:process.env,encoding:'utf8'});assert.equal(before.status,88,before.stderr);
 assert.equal((await p.terminalJournal(cfg).read(e)).publications.length,2,'uncommitted inactive frame must not replace the last valid result');
 const after=spawnSync(process.execPath,[process.argv[1],'crash-after'],{env:process.env,encoding:'utf8'});assert.equal(after.status,89,after.stderr);
 const recovered=await p.terminalJournal(cfg).read(e);
 assert.equal(recovered.publications.length,3);assert.equal(recovered.publications[2].event.eventId,'crash-after');
 await fs.unlink(path.join(root,'node-fill'));
 const sent=[];await p.terminalJournal(cfg).flush({async command(...args){sent.push(args);return[1,'1-0']}},e);
 const final=await p.terminalJournal(cfg).read(e);
 assert.ok(final.publications[1].event.text.includes(final.publications[1].event.resultBodyRef));
 const artifact=path.join(cfg.sharedDir,final.publications[1].event.resultBodyRef.replace(/^\/team\//,''));
 assert.equal(await fs.readFile(artifact,'utf8'),publicText);
 assert.equal(sent.length,3);
 const escapedText='\u0000\\"'.repeat(Math.floor((4*1024*1024-2048)/10));
 const encodedBytes=Buffer.byteLength(JSON.stringify({text:escapedText}));
 assert.ok(encodedBytes<4*1024*1024&&encodedBytes>4*1024*1024-4096);
 filler=await fs.open(path.join(root,'node-fill'),'w');
 try{for(;;)await filler.write(Buffer.alloc(1024*1024,120))}catch(error){assert.equal(error.code,'ENOSPC')}
 await filler.sync();await filler.close();filler=null;
 await j.stage(e,{event:'reply',eventKind:'delivery_supplement',nativePublicDelivery:true,eventId:'escaped-public',text:escapedText});
 assert.equal((await p.terminalJournal(cfg).read(e)).publications[3].event.text,escapedText);
 await fs.unlink(path.join(root,'node-fill'));
 await j.flush({async command(){return[1,'4-0']}},e);
 const escaped=(await j.read(e)).publications[3].event;
 assert.equal(await fs.readFile(path.join(cfg.sharedDir,escaped.resultBodyRef.replace(/^\/team\//,'')),'utf8'),escapedText);
 console.log(JSON.stringify({runtime:'openclaw',realENOSPC:true,actualCompletionTool:true,inboxBytes:64000,toolBodyBytes:completion.length,nativeBodyBytes:publicText.length,escapedResponseJSONBytes:encodedBytes,crashBeforeCommit:'previous outcome retained',crashAfterCommit:'new outcome retained',materializedAfterRecovery:true,businessCalls:1}));
}finally{await filler?.close();await fs.rm(path.join(root,'node-fill'),{force:true});}
