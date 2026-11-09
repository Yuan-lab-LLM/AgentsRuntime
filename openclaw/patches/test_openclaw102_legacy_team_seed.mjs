import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
const version=JSON.parse(fs.readFileSync('/usr/local/lib/node_modules/openclaw/package.json')).version;
assert.equal(version,'2026.8.1');
const home='/workspaces/openclaw/user-1/instance-900/home', state=home+'/.openclaw';
fs.mkdirSync(state,{recursive:true});
fs.writeFileSync(state+'/openclaw.json',JSON.stringify({gateway:{mode:'local'},plugins:{entries:{'redis-team':{enabled:true}}}}));
// Match the old image's accepted archive install, not 8.1's folder-copy path.
const archiveDir=fs.mkdtempSync('/tmp/legacy-team-pack-');
const pack=spawnSync('npm',['pack','--json','--pack-destination',archiveDir],{cwd:'/defaults/.openclaw/extensions/redis-team',encoding:'utf8',timeout:120000});
assert.equal(pack.status,0,'could not pack the accepted old managed plugin');
const archive=path.join(archiveDir,JSON.parse(pack.stdout)[0].filename);
const result=spawnSync('openclaw',['plugins','install',archive,'--force','--accept-capabilities'],{env:{...process.env,HOME:home,OPENCLAW_STATE_DIR:state,OPENCLAW_CONFIG_PATH:state+'/openclaw.json'},encoding:'utf8',timeout:120000});
assert.equal(result.status,0,'official old-core local Team installation failed: '+(result.stderr||result.stdout).slice(-2500));
function own(dir){fs.chownSync(dir,200900,100);for(const e of fs.readdirSync(dir,{withFileTypes:true})){const f=path.join(dir,e.name);if(e.isDirectory())own(f);else fs.lchownSync(f,200900,100);}}
own(home);
console.log('PASS seeded actual8.1 installed Team metadata, not a fabricated SQL install record.');
process.exit(0);
