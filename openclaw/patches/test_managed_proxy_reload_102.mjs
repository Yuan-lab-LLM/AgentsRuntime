import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
const root=process.env.OPENCLAW_PACKAGE_ROOT||'/usr/local/lib/node_modules/openclaw';
const dist=path.join(root,'dist');
const file=fs.readdirSync(dist).find(n=>/^server-runtime-config-.*\.mjs$/.test(n)&&fs.readFileSync(path.join(dist,n),'utf8').includes('function assertGatewayRuntimeSecurityConfig(params)'));
const {t:validate}=await import(pathToFileURL(path.join(dist,file)));
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cm-reload-policy-'));
const original={...process.env};
try {
 const policy=path.join(dir,'policy.json');
 process.env.CLAWMANAGER_PROXY_POLICY_FILE=policy;process.env.CLAWMANAGER_NETWORK_ISOLATED='1';
 const cfg={gateway:{auth:{mode:'trusted-proxy',trustedProxy:{userHeader:'x-forwarded-prefix'}},controlUi:{allowedOrigins:['https://portal.example']}}};
 const params={cfg,bindHost:'127.0.0.1',port:20000,controlUiEnabled:true,resolvedAuth:{mode:'trusted-proxy',trustedProxy:{userHeader:'x-forwarded-prefix'}},tailscaleMode:'off'};
 fs.writeFileSync(policy,JSON.stringify({contract:1,proxies:['192.0.2.10'],expires_at_ms:Date.now()+60000}));
 validate(params);
 assert.equal(cfg.gateway.trustedProxies,undefined,'reload persisted ephemeral authority');
 fs.writeFileSync(policy,JSON.stringify({contract:1,proxies:['192.0.2.10'],expires_at_ms:Date.now()-1}));
 assert.throws(()=>validate(params),/PROXY_POLICY_NOT_READY/);
 fs.unlinkSync(policy);assert.throws(()=>validate(params),/PROXY_POLICY_NOT_READY/);
 delete process.env.CLAWMANAGER_PROXY_POLICY_FILE;delete process.env.CLAWMANAGER_NETWORK_ISOLATED;
 assert.throws(()=>validate(params),/trustedProxies/,'ordinary validation must remain unchanged');
 console.log('PASS: installed 10.2 reload uses leased policy, rejects expiry/missing, preserves raw config and ordinary validation');
} finally { for(const k of ['CLAWMANAGER_PROXY_POLICY_FILE','CLAWMANAGER_NETWORK_ISOLATED']) {if(original[k]===undefined)delete process.env[k];else process.env[k]=original[k];}fs.rmSync(dir,{recursive:true,force:true}); }
