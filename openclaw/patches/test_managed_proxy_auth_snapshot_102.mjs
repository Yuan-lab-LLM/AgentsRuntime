// Installed 10.2 acceptance: the real auth resolver must see the overlay even
// when the native secrets manager owns a committed raw configuration snapshot.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import vm from 'node:vm';
import { pathToFileURL } from 'node:url';
import { withManagedProxyPolicy } from './managed_proxy_policy.mjs';
const root = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
assert.equal(JSON.parse(fs.readFileSync(path.join(root,'package.json'),'utf8')).version,'2026.10.2');
const dist=path.join(root,'dist');
const names=fs.readdirSync(dist);
const start=names.filter(n=>/^server-start-.*\.mjs$/.test(n));
assert.equal(start.length,1);
const source=fs.readFileSync(path.join(dist,start[0]),'utf8');
const getter=source.match(/const getResolvedAuth = \(\) => resolveGatewayAuth\(\{[\s\S]*?\n\t\}\);/g);
assert.equal(getter?.length,1,'exact native auth snapshot getter required');
const authPath=source.match(/from "(\.\/auth-resolve-[^"]+\.mjs)";/)?.[1];
assert.ok(authPath);
const {r:resolveGatewayAuth}=await import(pathToFileURL(path.join(dist,authPath)));
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cm-auth-snapshot-'));
try {
 const now=100000,file=path.join(dir,'policy.json');
 fs.writeFileSync(file,JSON.stringify({contract:1,proxies:['192.0.2.10'],expires_at_ms:now+60000}));
 const config={gateway:{auth:{mode:'trusted-proxy',password:'fake-local-only',trustedProxy:{userHeader:'x-forwarded-prefix',requiredHeaders:['x-forwarded-proto'],allowUsers:['instance-42']}}}};
 for(const snapshot of [config, {gateway:{}}, undefined]) for(const [isolated,time,allowed] of [[true,now,true],[true,now+60000,false],[false,now,false]]){
  const env={CLAWMANAGER_PROXY_POLICY_FILE:file,...(isolated?{CLAWMANAGER_NETWORK_ISOLATED:'1'}:{})};
  const overlay=cfg=>withManagedProxyPolicy(cfg,env,time);
  const sandbox={resolveGatewayAuth,clawmanagerAuthProxyPolicy:overlay,
   getActiveSecretsRuntimeConfigSnapshot:()=>snapshot && ({config:snapshot}),getRuntimeConfig:()=>overlay(config),
   resolvedStartupAuthOverride:undefined,process:{env},tailscaleMode:'off'};
  const auth=vm.runInNewContext(getter[0]+'\ngetResolvedAuth();',sandbox);
  assert.equal(auth.trustedProxy?.allowLoopback===true,allowed,'native committed snapshot bypassed protected relay policy');
  assert.equal(auth.trustedProxy?.userHeader,'x-forwarded-prefix');
  assert.deepEqual(auth.trustedProxy?.allowUsers,['instance-42']);
  assert.equal(config.gateway.auth.trustedProxy.allowLoopback,undefined,'raw writer-owned snapshot mutated');
 }
 console.log('PASS: actual installed native auth resolver, committed secrets snapshot, relay-only loopback and expiry');
} finally {fs.rmSync(dir,{recursive:true,force:true});}
