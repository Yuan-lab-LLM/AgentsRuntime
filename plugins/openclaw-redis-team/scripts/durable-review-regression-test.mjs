import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
const root=await fs.mkdtemp(path.join(process.env.RUNTIME_ENOSPC_ROOT||os.tmpdir(),'durable-review-'));
process.env.XDG_STATE_HOME=root;
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry = x => x;');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport { terminalJournal, createRuntime };\n//# sourceURL=durable-review-protocol.mjs').toString('base64')}`);
const cfg={teamId:'17',memberId:'worker',sharedDir:path.join(root,'shared')};
const e={messageId:'review-1',taskId:'root',assignmentId:'work',revision:1};
const journal=p.terminalJournal(cfg);let open=fs.open;
try{
 await journal.admit(e);await journal.started(e);
 const event={event:'completion_proposed',eventId:'outcome',completionId:'complete',attemptId:'stable-attempt',resultMarkdown:'completed external action'};
 fs.open=async function(file,flags,...rest){if(String(file).includes('terminal-delivery')&&['w','wx','a'].includes(flags)){const error=Error('No space left on device');error.code='ENOSPC';throw error;}return open.call(this,file,flags,...rest)};
 await assert.doesNotReject(journal.stage(e,event),'an allocated reservation must save a bounded result without allocating a replacement file');
 fs.open=open;
 const after=await p.terminalJournal(cfg).read(e);assert.equal(after.publications[0].event.resultMarkdown,event.resultMarkdown);
 const large={messageId:'large',taskId:'root',assignmentId:'work',revision:1};
 const text='Actual public answer. '.repeat(4000);
 const publication=await journal.stage(large,{event:'reply',eventKind:'delivery_supplement',nativePublicDelivery:true,eventId:'public-answer',text,
  teamId:'17',memberId:'worker',sourceMessageId:'original-input',sourceRecordId:'msg_answer',nativeInputId:'msg_input',assignmentId:'work',revision:1,
  nonAuthoritative:true,stateEffect:'none',visibleToChat:true});
 assert.ok(publication.event.text?.includes(publication.event.resultBodyRef),'actual CM projector requires a visible full-body link in text');
 assert.equal(publication.event.resultBodyBytes,Buffer.byteLength(text));
 const artifact=path.join(cfg.sharedDir,publication.event.resultBodyRef.replace(/^\/team\//,''));
 assert.equal(await fs.readFile(artifact,'utf8'),text);
 const overflow={messageId:'oversize-contract',taskId:'root',assignmentId:'work',revision:1};
 const args={status:'succeeded',summary:'Already executed',resultMarkdown:'x'.repeat(1024*1024+1)};
 const runtime=p.createRuntime({config:{channels:{'redis-team':{accounts:{default:{...cfg,enabled:true,fromEnv:false,redisUrl:'redis://127.0.0.1:1'}}}}},logger:{warn(){},info(){},error(){}}});
 await journal.admit(overflow);await journal.started(overflow);
 await assert.rejects(runtime.withActiveEnvelope(overflow,()=>runtime.completeTask(args),cfg),/1 MiB/);
 const retained=await journal.read(overflow), rawEvent=retained.publications[0].event;
 assert.equal(retained.state,'started');
 const rawProposal=rawEvent.resultBodyRef?await fs.readFile(path.join(cfg.sharedDir,rawEvent.resultBodyRef.replace(/^\/team\//,'')),'utf8'):rawEvent.resultMarkdown;
 assert.equal(JSON.parse(rawProposal).resultMarkdown,args.resultMarkdown);
 if(process.env.RUNTIME_PUBLIC_FIXTURE){await fs.writeFile(process.env.RUNTIME_PUBLIC_FIXTURE,JSON.stringify({event:publication.event,body:text,sharedDir:cfg.sharedDir}));}
 console.log('PASS preallocated result update and supported large public text/reference');
}finally{fs.open=open;if(!process.env.RUNTIME_PUBLIC_FIXTURE)await fs.rm(root,{recursive:true,force:true});}
