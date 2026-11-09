import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
const instanceId=Number(process.env.UPGRADE_INSTANCE_ID || 910);
const rolloutId=process.env.UPGRADE_TEST_ROLLOUT||'isolated-102-'+Date.now();
const root='/workspaces', workspace=root+'/openclaw/user-1/instance-'+instanceId, state=workspace+'/home/.openclaw';
const uid=200000+instanceId, gid=uid, token='isolated-upgrade-control';
if(process.env.UPGRADE_SHARED_PLUGINS==='1') {
 function links(source,target,depth=0) {
  fs.mkdirSync(target,{recursive:true});
  for(const e of fs.readdirSync(source,{withFileTypes:true})) {
   const a=path.join(source,e.name),b=path.join(target,e.name);
   if(depth>=3 && !e.name.startsWith('@'))fs.symlinkSync(fs.realpathSync(a),b);
   else if(e.isDirectory())links(a,b,depth+1);
   else if(e.isSymbolicLink())fs.symlinkSync(fs.realpathSync(a),b);
   else fs.copyFileSync(a,b);
  }
 }
 if(!fs.existsSync(state+'/npm')) links('/defaults/.openclaw/npm',state+'/npm');
 if(process.env.UPGRADE_DANGLING_IMAGE_LINK==='1') {
  const project='dingtalk-real-ai-dingtalk-connector-aa54111b45';
  const old=state+'/npm/projects/'+project+'/node_modules/bluebird';
  fs.mkdirSync(path.dirname(old),{recursive:true});
  if(!fs.existsSync(old)&&!fs.lstatSync(old,{throwIfNoEntry:false}))fs.symlinkSync('/defaults/.openclaw/npm/projects/'+project+'/node_modules/bluebird',old);
  if(process.env.UPGRADE_EXPECT_CONVERGENCE==='1'){
   const base=state+'/npm/projects/'+project;
   for(const file of ['package.json','package-lock.json']){const x=JSON.parse(fs.readFileSync(base+'/'+file));if(file==='package.json')x.dependencies['@dingtalk-real-ai/dingtalk-connector']='0.8.25';else {x.packages[''].dependencies['@dingtalk-real-ai/dingtalk-connector']='0.8.25';x.packages['node_modules/@dingtalk-real-ai/dingtalk-connector'].version='0.8.25';x.packages['node_modules/bluebird']={version:'3.4.7'};x.packages['node_modules/path-is-absolute']={version:'1.0.1'}}fs.writeFileSync(base+'/'+file,JSON.stringify(x));}
   const second=base+'/node_modules/path-is-absolute';if(!fs.lstatSync(second,{throwIfNoEntry:false}))fs.symlinkSync('/defaults/.openclaw/npm/projects/'+project+'/node_modules/path-is-absolute',second);
  }
 }
 const cfg=JSON.parse(fs.readFileSync(state+'/openclaw.json'));
 cfg.plugins={load:{paths:['/usr/local/lib/node_modules/openclaw/dist/extensions/redis-team']},entries:{'redis-team':{enabled:false},feishu:{enabled:false},'wecom-openclaw-plugin':{enabled:false}}};
 fs.writeFileSync(state+'/openclaw.json',JSON.stringify(cfg));
}
function files(dir, result=new Map(),base=dir) {
  for(const e of fs.readdirSync(dir,{withFileTypes:true})) { const f=path.join(dir,e.name); if(e.isDirectory())files(f,result,base); else if(e.isFile())result.set(path.relative(base,f),createHash('sha256').update(fs.readFileSync(f)).digest('hex')); }
  return result;
}
function own(dir){fs.chownSync(dir,uid,gid);for(const e of fs.readdirSync(dir,{withFileTypes:true})){const f=path.join(dir,e.name);if(e.isDirectory())own(f);else fs.lchownSync(f,uid,gid);}}
own(workspace);fs.chmodSync(root,0o755);
const sourceBefore=files(state);
const original=`${root}/.clawmanager-upgrades/rollout-${rolloutId}/instance-${instanceId}/preserved-rebuild/original`;
const before=process.env.UPGRADE_TEST_ROLLOUT?files(original):sourceBefore;
let registeredCapabilities=[];
const backend=http.createServer((req,res)=>{let body='';req.on('data',part=>body+=part);req.on('end',()=>{try{const payload=JSON.parse(body);if(Array.isArray(payload.capabilities))registeredCapabilities=payload.capabilities;}catch{}res.setHeader('content-type','application/json');res.end(JSON.stringify({success:true,data:{pod_id:1}}));});});
await new Promise(r=>backend.listen(19091,'127.0.0.1',r));
const agent=spawn('/usr/local/bin/clawmanager-agent',[],{env:{...process.env,
 CLAWMANAGER_RUNTIME_TYPE:'openclaw',CLAWMANAGER_OPENCLAW_VERSION:'2026.10.2',RUNTIME_WORKSPACE_ROOT:root,
 RUNTIME_AGENT_DATA_DIR:root+'/test-agent-control',RUNTIME_AGENT_CONTROL_TOKEN:token,RUNTIME_AGENT_REPORT_TOKEN:token,
 POD_NAMESPACE:'isolated-upgrade',POD_NAME:'target',POD_UID:'isolated-target-uid',POD_IP:'127.0.0.1',
 CLAWMANAGER_BACKEND_URL:'http://127.0.0.1:19091',RUNTIME_AGENT_LISTEN_ADDR:'127.0.0.1:19090',
},stdio:['ignore','pipe','pipe']});
let logs='';for(const s of [agent.stdout,agent.stderr])s.on('data',x=>logs=(logs+x).slice(-18000));
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const req={rollout_id:rolloutId,instance_id:instanceId,user_id:1,generation:1,lease_token:'isolated-writer-only',uid,gid};
if(process.env.UPGRADE_PRESERVE_REBUILD==='1')req.recovery_mode='rebuild';
async function control(route,body) {
 return await new Promise((resolve,reject)=>{
  const request=http.request('http://127.0.0.1:19090'+route,{method:body?'POST':'GET',headers:{'X-ClawManager-Control-Token':token,'content-type':'application/json'},timeout:16*60000},response=>{
   let raw='';response.setEncoding('utf8');response.on('data',chunk=>raw+=chunk);
   response.on('end',()=>{try {assert.ok(response.statusCode===200||response.statusCode===201,`${route}: ${response.statusCode} ${raw}`);resolve(raw?JSON.parse(raw):{});}catch(err){reject(err);}});
  });
  request.on('error',reject);request.on('timeout',()=>request.destroy(Error('Local Agent control request timed out')));
  request.end(body?JSON.stringify(body):undefined);
 });
}
try {
 for(let i=0;i<80;i++){try{await control('/v1/health');break;}catch{if(agent.exitCode!==null)throw Error(logs);await pause(250);}}
 if(req.recovery_mode==='rebuild') {
  for(let i=0;i<50&&!registeredCapabilities.includes('openclaw.rebuild-preserve-v2');i++)await pause(100);
  assert.ok(registeredCapabilities.includes('openclaw.rebuild-preserve-v2'),'actual Runtime registration lacks the repaired rebuild contract');
 }
 if(process.env.UPGRADE_TEST_ROLLOUT){
  // A prior failing fixture invocation may have left this synthetic lease.
  // Release through the real identity-checked API, never edit/delete its file.
  await control('/v1/openclaw/writer-leases/release',{...req,rollout_id:rolloutId+'-retry',generation:2,lease_token:'isolated-retry-writer'}).catch(()=>{});
 }else{
 if(process.env.UPGRADE_DANGLING_IMAGE_LINK==='1'&&process.env.UPGRADE_EXPECT_CONVERGENCE!=='1') {
  await assert.rejects(control('/v1/openclaw/upgrade/preflight',{...req,recovery_mode:'retry'}),/OPENCLAW_PLUGIN_REBUILD_REQUIRED/);
  console.log('PASS broken legacy image dependency fails ordinary preflight before any live mutation');
 }
 const preflight=await control('/v1/openclaw/upgrade/preflight',req);
 assert.equal(preflight.status,'compatible');
 if(process.env.UPGRADE_EXPECT_CONVERGENCE==='1'){assert.match(preflight.managed_plugin_plan_sha256,/^[a-f0-9]{64}$/);req.managed_plugin_plan_sha256=preflight.managed_plugin_plan_sha256;assert.equal(req.recovery_mode,undefined,'ordinary upgrade must not rely on explicit rebuild');}
 assert.deepEqual(files(state),sourceBefore,'preflight changed live source control data');
 console.log('PASS: actual Agent preflight isolates multi-Agent absolute custom state and does not change source bytes');
 }
 await control('/v1/openclaw/writer-leases/acquire',{...req,ttl_seconds:1800});
 const migrated=await control('/v1/openclaw/session-sqlite/migrate',req);
 assert.equal(migrated.status,'validated');assert.equal(migrated.session_count,2);
 if(req.recovery_mode==='rebuild'||process.env.UPGRADE_EXPECT_CONVERGENCE==='1') {
  assert.equal(migrated.data_preserved,true);assert.match(migrated.preservation_sha256,/^[a-f0-9]{64}$/);
  const saved=original;
  for(const [name,hash]of before)assert.equal(createHash('sha256').update(fs.readFileSync(path.join(saved,name))).digest('hex'),hash,`original data lost: ${name}`);
  if(process.env.UPGRADE_DANGLING_IMAGE_LINK==='1') {
   const rel='npm/projects/dingtalk-real-ai-dingtalk-connector-aa54111b45/node_modules/bluebird';
   assert.equal(fs.readlinkSync(saved+'/'+rel),'/defaults/.openclaw/'+rel);
   assert.equal(fs.lstatSync(state+'/'+rel,{throwIfNoEntry:false}),undefined,'fresh environment retained obsolete image alias');
   console.log('PASS original dangling link retained while actual target SDK uses a fresh plugin environment');
  }
  console.log('PASS preserved rebuild: complete original state retained, same workspace and real SDK history identities');
  if(process.env.UPGRADE_EXPECT_CONVERGENCE==='1'){for(const file of ['package.json','package-lock.json'])assert.ok(fs.readFileSync(state+'/npm/projects/dingtalk-real-ai-dingtalk-connector-aa54111b45/'+file,'utf8').includes('0.8.26'));console.log('PASS ordinary automatic plugin convergence, version/lock alignment and preserved original');}
 }
 assert.deepEqual(await control('/v1/openclaw/session-sqlite/migrate',req),migrated,'retry repeated migration rather than returning durable receipt');
 const checked=await new Promise(resolve=>{const child=spawn('node',['/tests/test_openclaw102_state_fixture.mjs','verify']);let stdout='',stderr='';child.stdout.on('data',x=>stdout+=x);child.stderr.on('data',x=>stderr+=x);const timer=setTimeout(()=>child.kill('SIGKILL'),120000);child.on('exit',status=>{clearTimeout(timer);resolve({status,stdout,stderr})})});
 assert.equal(checked.status,0,checked.stderr+checked.stdout);console.log(checked.stdout.trim());
 if(process.env.UPGRADE_RETRY_CHAIN==='1'){
  await control('/v1/openclaw/writer-leases/release',req);
  const child={...req,rollout_id:rolloutId+'-retry',generation:2,recovery_mode:'retry',resume_rollout_id:req.rollout_id,resume_generation:1,lease_token:'isolated-retry-writer'};
  await control('/v1/openclaw/writer-leases/acquire',{...child,ttl_seconds:1800});
  const resumed=await control('/v1/openclaw/session-sqlite/migrate',child);
  assert.equal(resumed.status,'validated');assert.equal(resumed.session_count,2);assert.equal(resumed.phase_durations_ms?.reused_validated_migration,0);
  await control('/v1/openclaw/writer-leases/release',child);
  await control('/v1/openclaw/writer-leases/acquire',{...req,ttl_seconds:1800});
  console.log('PASS new recovery generation reuses the original validated migration without replaying Doctor');
 }
 const restored=await control('/v1/openclaw/session-sqlite/restore',{...req,preserve_session_sqlite:true});
 assert.equal(restored.status,'restored');assert.equal(restored.state_restored,true);
 const after=files(state);
 for(const [name,hash] of before) if(name.endsWith('.sqlite')||name.endsWith('-wal'))assert.equal(after.get(name),hash,`rollback did not restore matching old DB ${name}`);
 console.log('PASS: migration receipt retry, target history, pre-activation complete matching old database rollback');
 await control('/v1/openclaw/writer-leases/release',req);
} catch(e) {console.error(logs);throw e;}
finally {
 await control('/v1/openclaw/writer-leases/release',{...req,rollout_id:rolloutId+'-retry',generation:2,lease_token:'isolated-retry-writer'}).catch(()=>{});
 await control('/v1/openclaw/writer-leases/release',req).catch(()=>{});
 agent.kill('SIGTERM');await Promise.race([new Promise(r=>agent.once('exit',r)),pause(5000)]);if(agent.exitCode===null)agent.kill('SIGKILL');backend.close();
}
