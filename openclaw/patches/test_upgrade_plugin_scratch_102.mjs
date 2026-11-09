import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
const {i:resolve}=await import('/usr/local/lib/node_modules/openclaw/dist/plugin-source-capture-path-Dqh0vrxU.mjs');
const root=fs.mkdtempSync('/tmp/clawmanager-upgrade-plugin-');
try {
 delete process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT;
 assert.equal(resolve('/persistent/state'),'/persistent/state/tmp/plugin-captures');
 process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT=root;
 assert.equal(resolve('/persistent/state'),root);
 fs.chmodSync(root,0o777);assert.throws(()=>resolve('/persistent/state'));fs.chmodSync(root,0o700);
 process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT='/tmp';assert.throws(()=>resolve('/persistent/state'));
 const link=root+'-alias';fs.symlinkSync(root,link);
 process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT=link;assert.throws(()=>resolve('/persistent/state'));fs.unlinkSync(link);
 console.log('PASS: normal path unchanged, owned scratch admitted, broad path/mode/symlink rejected');
}finally {delete process.env.CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT;fs.rmSync(root,{recursive:true,force:true});}
