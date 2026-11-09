// Only the already seeded disposable fixture; use the old core's metadata API.
import fs from 'node:fs';
import assert from 'node:assert/strict';
const id=process.env.UPGRADE_INSTANCE_ID || '911';
const workspace='/workspaces/openclaw/user-1/instance-'+id, home=workspace+'/home',state=home+'/.openclaw';
assert.ok(fs.existsSync(workspace+'/seed-2026.8.1.json'));
assert.equal(JSON.parse(fs.readFileSync('/usr/local/lib/node_modules/openclaw/package.json')).version,'2026.8.1');
Object.assign(process.env,{HOME:home,OPENCLAW_STATE_DIR:state,OPENCLAW_CONFIG_PATH:state+'/openclaw.json'});
const cfg=JSON.parse(fs.readFileSync(state+'/openclaw.json'));
cfg.plugins={entries:{feishu:{enabled:false},'wecom-openclaw-plugin':{enabled:false}}};
fs.writeFileSync(state+'/openclaw.json',JSON.stringify(cfg));
const records={
 feishu:{source:'npm',spec:'@openclaw/feishu',version:'2026.8.1',installPath:state+'/npm/projects/openclaw-feishu-dc69f44688/node_modules/@openclaw/feishu'},
 'wecom-openclaw-plugin':{source:'npm',spec:'@wecom/wecom-openclaw-plugin',version:'2026.8.17',installPath:state+'/npm/projects/wecom-wecom-openclaw-plugin-18f843d908/node_modules/@wecom/wecom-openclaw-plugin'},
};
if(process.env.UPGRADE_WITH_DINGTALK==='1')records['dingtalk-connector']={source:'npm',spec:'@dingtalk-real-ai/dingtalk-connector@0.8.25',version:'0.8.25',installPath:state+'/npm/projects/dingtalk-real-ai-dingtalk-connector-aa54111b45/node_modules/@dingtalk-real-ai/dingtalk-connector'};
const {s:write}=await import('/usr/local/lib/node_modules/openclaw/dist/installed-plugin-index-records-BC7x_YBU.js');
await write(records,{env:process.env,config:cfg});
console.log('PASS: actual8.1 persisted plugin index records, with no retired config keys');
process.exit(0);
