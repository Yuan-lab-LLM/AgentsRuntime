import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
const root=await fs.mkdtemp(path.join(os.tmpdir(),'team-capacity-'));process.env.XDG_STATE_HOME=root;
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry = x => x;');
const p=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport { terminalJournal, eventStreamFields };').toString('base64')}`);
const cfg={teamId:'capacity',memberId:'worker',sharedDir:path.join(root,'shared')};
let sequence=0,closure=null,acceptedProof=null;
const redis={async command(...args){if(args[0]==='EVAL')return [1,`${++sequence}-0`];if(String(args[1]).endsWith(':replay-source-closed'))return closure&&JSON.stringify(closure);if(String(args[1]).includes(':completion-ack:'))return JSON.stringify(acceptedProof||{decision:'accepted'});return null;}};
try{
 const j=p.terminalJournal(cfg);
 for(let i=0;i<501;i++){
  const e={messageId:'m'+i,taskId:'t'+i,assignmentId:'a'+i,redisId:`${i+1}-0`};
  await j.admit(e);await j.started(e);
  if(i===0){const files=await fs.readdir(path.join(root,'terminal-delivery'));const slot=files.find(n=>n.endsWith('.json'));assert.ok((await fs.stat(path.join(root,'terminal-delivery',slot))).blocks*512>=12*1024*1024,'admission reserves physical blocks for both banks');}
  await j.stage(e,{event:'completion_proposed',eventId:'evt'+i,completionId:'c'+i,attemptId:'attempt'+i,status:'succeeded',resultMarkdown:'Actual outcome '+i});
  await j.accepted(e);await j.flush(redis,e,true);
  assert.equal((await j.read(e)).compact,true);
 }
 const files=(await fs.readdir(path.join(root,'terminal-delivery'))).filter(n=>n.endsWith('.json'));
 assert.equal(files.length,501);assert.ok((await fs.stat(path.join(root,'terminal-delivery',files[0]))).size===4096);
 assert.ok((await fs.stat(path.join(root,'terminal-delivery',files[0]))).blocks*512<=8192,'completed compaction actually frees the reserved physical blocks');
 assert.equal((await j.admit({messageId:'m0'})).state,'accepted','compact duplicate retains outcome identity');
 const first=await j.read({messageId:'m0'});assert.ok(first.resultRef);
 for(let i=0;i<256;i++)await j.admit({messageId:'pending'+i,taskId:'pending'+i,redisId:`${i+1}-0`});
 await assert.rejects(j.admit({messageId:'overflow'}),/capacity/);
 closure={schema:1,closed_before_ms:99999,committed_cursor:'99999-0',proof_sha256:'a'.repeat(64),verified_at:new Date().toISOString()};
 for(let i=0;i<6;i++)await j.flush(redis);
 assert.equal(await j.read({messageId:'m0'}),null,'only authority-closed completed receipts are reclaimed');
 assert.equal((await j.read({messageId:'pending0'})).state,'admitted','unresolved source is never reclaimed');
 const other=p.terminalJournal({...cfg,memberId:'other'});
 const e={messageId:'large',taskId:'large'};await other.admit(e);
 const body='完整结果'.repeat(30000);
 const publication=await other.stage(e,{event:'completion_proposed',eventId:'large',completionId:'large',attemptId:'large',resultMarkdown:body,result:body});
 assert.ok(publication.event.resultBodyRef);assert.equal(publication.event.resultBodyBytes,Buffer.byteLength(body));
 assert.ok(p.eventStreamFields(publication.event).reduce((n,v)=>n+Buffer.byteLength(String(v)),0)<65536);
 const artifact=path.join(cfg.sharedDir,publication.event.resultBodyRef.replace(/^\/team\//,''));
 assert.equal(await fs.readFile(artifact,'utf8'),body);
 closure=null;
 const multi=p.terminalJournal({...cfg,memberId:'multi'});
 for(let i=0;i<300;i++){
  const earlier={messageId:'earlier'+i,taskId:'scope'+i,rootTaskId:'scope'+i,assignmentId:'assignment'+i,revision:1};
  const later={...earlier,messageId:'later'+i};
  await multi.admit(earlier);await multi.started(earlier);
  await multi.admit(later);await multi.started(later);
  acceptedProof={teamId:cfg.teamId,memberId:'multi',rootTaskId:later.rootTaskId,completionId:'closed'+i,attemptId:'attempt'+i,decision:'accepted'};
  await multi.stage(later,{event:'completion_proposed',eventId:'later'+i,completionId:acceptedProof.completionId,attemptId:acceptedProof.attemptId,assignmentId:later.assignmentId,revision:1,status:'succeeded'});
  await multi.accepted(later);await multi.flush(redis,later,true);await multi.flush(redis,earlier,true);
  assert.equal((await multi.read(earlier)).compact,true);assert.equal((await multi.read(earlier)).executionMayHaveOccurred,true);
 }
 for(const change of [{rootTaskId:'wrong-root'},{assignmentId:'wrong-assignment'},{revision:2}]){
  const wrong={messageId:'mismatch'+JSON.stringify(change),taskId:'scope299',rootTaskId:'scope299',assignmentId:'assignment299',revision:1,...change};
  await multi.admit(wrong);await multi.started(wrong);await multi.flush(redis,wrong,true);
  assert.equal((await multi.read(wrong)).state,'started','different root/assignment/revision must not inherit closure');
 }
 console.log(JSON.stringify({completed:501,compactBytes:4096,pendingCapacity:256,overflowRejected:true,verifiedClosureOnly:true,largeBodyFullArtifact:true,multiTurnAssignments:300,strictScopeFence:true}));
}finally{await fs.rm(root,{recursive:true,force:true});}
