// Executed only by the fenced upgrade command (or its isolated rehearsal).
// Use the pinned core's official record API, never guess its SQLite schema.
import fs from 'node:fs';
import path from 'node:path';
const state=path.resolve(process.env.OPENCLAW_STATE_DIR||'');
if(!state.endsWith(path.sep+'.openclaw')||!process.env.OPENCLAW_CONFIG_PATH)throw Error('explicit managed upgrade HOME required');
const core='/usr/local/lib/node_modules/openclaw';
if(JSON.parse(fs.readFileSync(core+'/package.json')).version!=='2026.10.2')throw Error('unsupported plugin record version');
const plan=JSON.parse(fs.readFileSync(state+'/.clawmanager-plugin-convergence.json'));
const {r:load}=await import(core+'/dist/installed-plugin-index-record-reader-DoGhN1Un.mjs');
const {a:write}=await import(core+'/dist/installed-plugin-index-records-CzJqH-fd.mjs');
const {i:withLease}=await import(core+'/dist/plugin-lifecycle-lease-5X2Of1QR.mjs');
const records=await load({env:process.env});
for(const change of plan.changes){
 if(!/^[A-Za-z0-9._-]+$/.test(change.project)||change.project==='.'||change.project==='..'||change.name.includes('..'))throw Error('invalid managed plugin identity');
 const install=path.join(state,'npm/projects',change.project,'node_modules',change.name);
 for(const record of Object.values(records)){
  if(record.source!=='npm'||path.resolve(record.installPath||'/')!==install)continue;
  if(record.version!==change.previous_version&&record.version!==change.version)throw Error('custom plugin record version requires review: '+change.name);
  record.version=change.version;
  for(const key of ['spec','resolvedSpec'])if(record[key]===change.name+'@'+change.previous_version)record[key]=change.name+'@'+change.version;
  if(record.resolvedVersion===change.previous_version)record.resolvedVersion=change.version;
  if(change.previous_version!==change.version){delete record.integrity;delete record.shasum;delete record.resolvedAt;}
 }
}
const config=JSON.parse(fs.readFileSync(process.env.OPENCLAW_CONFIG_PATH));
await withLease({env:process.env},lease=>write(records,{env:process.env,config,lease}));
console.log('Managed plugin metadata converged with pinned image payload');
