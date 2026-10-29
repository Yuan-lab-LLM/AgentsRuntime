// Installed 10.2 consumer test. Only config suppliers are injected; native
// resolution, route refresh, lifecycle state and missing-profile errors are real.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { pathToFileURL } from 'node:url';
import { isDeepStrictEqual } from 'node:util';
const root=process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
assert.equal(JSON.parse(fs.readFileSync(path.join(root,'package.json'),'utf8')).version,'2026.10.2');
const dist=path.join(root,'dist');
function source(prefix,needle) {
 const matches=fs.readdirSync(dist).filter(n=>n.startsWith(prefix)&&n.endsWith('.mjs')).map(n=>fs.readFileSync(path.join(dist,n),'utf8')).filter(s=>s.includes(needle));
 assert.equal(matches.length,1,'exact installed consumer required: '+prefix);return matches[0];
}
function fn(text,name) {
 const matches=text.match(new RegExp('(?:async )?function '+name+'\\([^]*?\\n\\}', 'g'));
 assert.equal(matches?.length,1,'exact native function required: '+name);return matches[0];
}
const contextSource=source('server-context-','function loadBrowserConfigForRuntimeRefresh()');
const controlSource=source('browser-control-state-','function createBrowserControlContext()');
const serviceSource=source('control-service-','function startBrowserControlServiceUnlocked()');
const configSource=source('io.runtime-','function getRuntimeConfig(options)');
assert.match(serviceSource,/const browserCfg = loadBrowserConfigForRuntimeRefresh\(\);/);
assert.match(fn(controlSource,'createBrowserControlContext'),/refreshConfigFromDisk: true/);
const config=(port,profile='interactive')=>({browser:{enabled:true,defaultProfile:profile,profiles:{[profile]:{cdpUrl:'http://192.0.2.10:'+port,attachOnly:true,driver:'openclaw',color:'#112233'}}}});
let raw=config(9223),runtime=config(9224);
const pinned=config(9222);
const env={CLAWMANAGER_NETWORK_ISOLATED:'1'};
// Exercise the real installed pin selector, not a mock getter that always
// returns fresh data. Runtime and source snapshots can both lag the disk.
const sandbox={process:{env},getRuntimeConfigSourceSnapshot:()=>raw,
 createConfigIO:()=>({loadConfig:()=>runtime}),loadPinnedRuntimeConfig:()=>pinned,
 clawmanagerManagedProxyPolicy:value=>value,isDeepStrictEqual};
vm.createContext(sandbox);
for(const name of ['loadConfig','getRuntimeConfig']) vm.runInContext(fn(configSource,name),sandbox);
vm.runInContext(fn(contextSource,'loadBrowserConfigForRuntimeRefresh'),sandbox);
assert.equal(sandbox.loadBrowserConfigForRuntimeRefresh(),runtime,'protected Browser consumer selected stale source snapshot');
for(const isolated of [undefined,'0','1']) {
 env.CLAWMANAGER_NETWORK_ISOLATED=isolated;
 assert.equal(sandbox.loadBrowserConfigForRuntimeRefresh(),isolated==='1'?runtime:raw);
 const saved=raw;raw=undefined;assert.equal(sandbox.loadBrowserConfigForRuntimeRefresh(),isolated==='1'?runtime:pinned);raw=saved;
}
// Import the installed native dependencies of the exercised consumer functions.
for(const dependency of ['resolveBrowserConfig','getProfileLifecycle','BrowserProfileNotFoundError']) {
 const statement=[...contextSource.matchAll(/import \{ ([^\n]+) \} from "([^"]+)";/g)].find(m=>m[1].split(', ').some(binding=>binding.endsWith(' as '+dependency)));
 assert.ok(statement,'native dependency '+dependency);
 const module=await import(pathToFileURL(path.join(dist,statement[2])));
 for(const binding of statement[1].split(', ')) {const [exported,local]=binding.split(' as ');sandbox[local||exported]=module[exported];}
}
const lifecycleImport=controlSource.match(/from "(\.\/runtime-lifecycle-[^"]+\.mjs)";/)?.[1];
assert.ok(lifecycleImport);
const {t:createBrowserRuntimeState}=await import(pathToFileURL(path.join(dist,lifecycleImport)));
for(const name of ['changedProfileInvariants','queueRemovedProfileCleanup','applyResolvedConfig','refreshResolvedBrowserConfigFromDisk','createBrowserRouteContext']) vm.runInContext(fn(contextSource,name),sandbox);
env.CLAWMANAGER_NETWORK_ISOLATED='1';
const state=await createBrowserRuntimeState({server:null,port:19341,resolved:sandbox.resolveBrowserConfig(raw.browser,raw)});
const cached=sandbox.getOrCreateProfileRuntime(state,sandbox.resolveProfile(state.resolved,'interactive'));
const route=sandbox.createBrowserRouteContext({getState:()=>state,refreshConfigFromDisk:true});
assert.equal(sandbox.resolveProfile(route.state().resolved,'interactive').cdpPort,9224,'existing same-name profile retained old endpoint');
assert.equal(cached.profile.cdpPort,9224,'cached native profile retained old endpoint');
await new Promise(resolve=>setImmediate(resolve));
runtime=config(9225);
assert.equal(sandbox.resolveProfile(route.state().resolved,'interactive').cdpPort,9225,'subsequent refresh revived old endpoint');
assert.equal(cached.profile.cdpPort,9225,'cached native profile revived old endpoint');
await new Promise(resolve=>setImmediate(resolve));
assert.throws(()=>route.forProfile('unknown'),/not found/,'unknown profile invented a fallback');
runtime=config(9226,'replacement');
assert.throws(()=>route.forProfile('interactive'),/not found/,'removed managed profile revived from stale source snapshot');
assert.equal(sandbox.resolveProfile(route.state().resolved,'replacement').cdpPort,9226);
await new Promise(resolve=>setImmediate(resolve));
assert.equal(state.profiles.has('interactive'),false,'native removal cleanup retained profile');
env.CLAWMANAGER_NETWORK_ISOLATED='0';
assert.equal(sandbox.resolveProfile(route.state().resolved,'interactive').cdpPort,9223,'legacy source preference changed');
assert.equal(raw.browser.profiles.interactive.cdpUrl,'http://192.0.2.10:9223','writer-owned source mutated');
console.log('PASS: installed Browser consumer chooses current protected config, preserves legacy/fallback, refreshes same-name profile and rejects unknown/removed profiles');

// The private broker needs a CDP-only grant. Browser navigation shares the
// original policy, so a global hostname allowlist would break public pages.
const nativeModule = async (prefix) => {
 const names=fs.readdirSync(dist).filter(n=>n.startsWith(prefix)&&n.endsWith('.mjs'));
 assert.equal(names.length,1);return import(pathToFileURL(path.join(dist,names[0])));
};
const cdpPolicyModule=contextSource.match(/from "(\.\/cdp-reachability-policy-[^"]+)";/)?.[1];
assert.ok(cdpPolicyModule);
const {r:resolveCdpPolicy}=await import(pathToFileURL(path.join(dist,cdpPolicyModule)));
const {p:resolvePinned,t:SsrFBlockedError}=await nativeModule('ssrf-DEF');
const host='clawmanager-local-services.invalid';
const original=sandbox.resolveBrowserConfig({enabled:true,defaultProfile:'interactive',ssrfPolicy:{allowedHostnames:[host]},profiles:{interactive:{cdpUrl:'http://'+host+':31282',attachOnly:true,driver:'openclaw',color:'#112233'}}},{});
const managedProfile=sandbox.resolveProfile(original,'interactive');
const before=JSON.stringify(original.ssrfPolicy),savedIsolated=process.env.CLAWMANAGER_NETWORK_ISOLATED;
try {
 process.env.CLAWMANAGER_NETWORK_ISOLATED='1';
 const cdp=resolveCdpPolicy(managedProfile,original.ssrfPolicy);
 assert.equal(cdp.dangerouslyAllowPrivateNetwork,true,'private broker did not receive CDP-only grant');
 assert.deepEqual(cdp.hostnameAllowlist,[host]);
 assert.equal(JSON.stringify(original.ssrfPolicy),before,'CDP grant mutated navigation policy');
 const helperModule=contextSource.match(/from "(\.\/cdp\.helpers-[^"]+)";/)?.[1];assert.ok(helperModule);
 const {n:assertEndpoint}=await import(pathToFileURL(path.join(dist,helperModule)));
 for(const discovered of ['ws://example.com:31282/devtools/browser/test','ws://'+host+':31283/devtools/browser/test']) await assert.rejects(assertEndpoint(discovered,cdp,{source:'discovered',configuredUrl:managedProfile.cdpUrl}),error=>error.cause instanceof SsrFBlockedError && error.cause.message==='discovered CDP endpoint changed configured authority');

 const lookupFn=async name=>[{address:name===host?'169.254.64.1':'93.184.216.34',family:4}];
 assert.deepEqual((await resolvePinned(host,{policy:cdp,lookupFn})).addresses,['169.254.64.1']);
 for(const denied of ['169.254.169.254','127.0.0.1','clawmanager-local-model.invalid','example.com']) await assert.rejects(resolvePinned(denied,{policy:cdp,lookupFn}),SsrFBlockedError);
 assert.deepEqual((await resolvePinned('example.com',{policy:original.ssrfPolicy,lookupFn})).addresses,['93.184.216.34'],'public navigation inherited CDP-only allowlist');
 await assert.rejects(resolvePinned('169.254.169.254',{policy:original.ssrfPolicy,lookupFn:async()=>[{address:'169.254.169.254',family:4}]}),SsrFBlockedError);
 for(const changes of [{cdpPort:31283},{cdpHost:'example.com'},{cdpUrl:'http://'+host+':31283'},{cdpUrl:'http://user@'+host+':31282'},{cdpUrl:'http://'+host+':31282/elsewhere'},{cdpUrl:'http://'+host+':31282?x=1'},{cdpUrl:'http://'+host+':31282#x'},{driver:'existing-session'},{attachOnly:false}]) assert.notEqual(resolveCdpPolicy({...managedProfile,...changes},original.ssrfPolicy)?.dangerouslyAllowPrivateNetwork,true);
 assert.notEqual(resolveCdpPolicy(managedProfile,{allowedHostnames:['example.com']})?.dangerouslyAllowPrivateNetwork,true);
 for(const value of ['0',undefined]) {
  if(value===undefined)delete process.env.CLAWMANAGER_NETWORK_ISOLATED;else process.env.CLAWMANAGER_NETWORK_ISOLATED=value;
  assert.notEqual(resolveCdpPolicy(managedProfile,original.ssrfPolicy)?.dangerouslyAllowPrivateNetwork,true);
 }
} finally {if(savedIsolated===undefined)delete process.env.CLAWMANAGER_NETWORK_ISOLATED;else process.env.CLAWMANAGER_NETWORK_ISOLATED=savedIsolated;}
console.log('PASS: native CDP policy grants only the exact private broker and preserves public navigation/private-denial semantics');
