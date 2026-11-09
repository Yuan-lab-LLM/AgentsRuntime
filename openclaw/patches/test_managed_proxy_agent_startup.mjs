// Disposable-container acceptance through the real Agent, config writer and
// OpenClaw binary. No live credentials or user workspaces are used.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import crypto from 'node:crypto';
import { lookup } from 'node:dns/promises';
import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
const require = createRequire('/usr/local/lib/node_modules/openclaw/package.json');
const coreVersion = require('/usr/local/lib/node_modules/openclaw/package.json').version;
const WebSocket = require('ws');
const root = process.env.TEST_MANAGED_STARTUP_WORKSPACE_ROOT || fs.mkdtempSync('/tmp/managed-startup-');
fs.chmodSync(root, 0o755);
const ip = Object.values(os.networkInterfaces()).flat().find(x => x.family === 'IPv4' && !x.internal).address;
// Match production's separate App/Runtime network identities. OpenClaw
// correctly rejects trusted-proxy claims originating from its own interface.
const proxyIP = (await lookup('proxy-startup-peer')).address;
const rotatedProxyIP = (await lookup('proxy-startup-peer-b')).address;
const token = 'isolated-control-test-only';
const backend = http.createServer((req,res) => { req.resume();res.setHeader('content-type','application/json');res.end(JSON.stringify({success:true,data:{pod_id:1}})); });
await new Promise(resolve => backend.listen(19091,'0.0.0.0',resolve));
let logs = '';
const agent = spawn('/usr/local/bin/clawmanager-agent', [], {env:{...process.env,
  CLAWMANAGER_RUNTIME_TYPE:'openclaw', CLAWMANAGER_OPENCLAW_VERSION:coreVersion,
  RUNTIME_GATEWAY_AUTH_MODE:'trusted-proxy', RUNTIME_AGENT_CONTROL_TOKEN:token, RUNTIME_AGENT_REPORT_TOKEN:token,
  POD_NAMESPACE:'isolated-startup', POD_NAME:'runtime-test', POD_IP:ip,
  RUNTIME_WORKSPACE_ROOT:root, RUNTIME_AGENT_DATA_DIR:path.join(root,'agent'),
  CLAWMANAGER_BACKEND_URL:'http://127.0.0.1:19091', RUNTIME_AGENT_LISTEN_ADDR:'127.0.0.1:19090',
},stdio:['ignore','pipe','pipe']});
for(const stream of [agent.stdout,agent.stderr])stream.on('data',x=>{logs=(logs+x).slice(-80000)});
const pause = ms => new Promise(r=>setTimeout(r,ms));
async function control(route,method='GET',body){
  const response=await fetch('http://127.0.0.1:19090'+route,{method,headers:{'X-ClawManager-Control-Token':token,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined,signal:AbortSignal.timeout(method==='DELETE'?35000:5000)});
  const raw=await response.text();assert.equal(response.status,200,raw);return raw?JSON.parse(raw):null;
}
async function policy(proxies=[proxyIP]){const old=await control('/v1/proxy-policy');return control('/v1/proxy-policy','PUT',{namespace:'isolated-startup',expected_revision:old.revision,proxies});}
function request(port,instance,localAddress=ip){return new Promise((resolve,reject)=>{
  const req=http.get({host:localAddress===ip?proxyIP:ip,port,path:`/api/v1/instances/${instance}/proxy/`,localAddress,headers:{'x-forwarded-for':'192.0.2.10','x-forwarded-proto':'http','x-forwarded-prefix':`/api/v1/instances/${instance}/proxy`}},res=>{let data='';res.on('data',x=>data+=x);res.on('end',()=>resolve({status:res.statusCode,body:data}));});
  req.setTimeout(3000,()=>req.destroy(new Error('timeout')));req.on('error',reject);
});}
function ws(port,id,source=ip,proxyTarget=proxyIP,keepOpen=false,diagnose=false){return new Promise((resolve,reject)=>{
  const keys=crypto.generateKeyPairSync('ed25519');
  const pub=keys.publicKey.export({format:'der',type:'spki'}).subarray(-32);
  const deviceId=crypto.createHash('sha256').update(pub).digest('hex');
  let done=false;
  const storageRetries = new Map();
  const socket=new WebSocket(`ws://${source===ip?proxyTarget:ip}:${port}/api/v1/instances/${id}/proxy/`,{localAddress:source,handshakeTimeout:5000,headers:{Origin:'http://127.0.0.1:19091','x-forwarded-for':'192.0.2.10','x-forwarded-proto':'http','x-forwarded-prefix':`/api/v1/instances/${id}/proxy`}});
  const timer=setTimeout(()=>{done=true;socket.terminate();reject(Error('authenticated WebSocket timeout'));},10000);
  const finish=(value,error)=>{if(done)return;done=true;clearTimeout(timer);if(!keepOpen || error)socket.close();if(error)reject(error);else resolve(keepOpen && value===101 ? socket : value);};
  socket.on('message',data=>{
    const message=JSON.parse(data);
    if(message.event==='connect.challenge'){
      const now=Date.now(),nonce=message.payload.nonce;
      const signed=['v3',deviceId,'openclaw-control-ui','webchat','operator','operator.read',String(now),'',nonce,'linux',''].join('|');
      socket.send(JSON.stringify({type:'req',id:'connect',method:'connect',params:{minProtocol:4,maxProtocol:4,client:{id:'openclaw-control-ui',version:'acceptance',platform:'linux',mode:'webchat'},role:'operator',scopes:['operator.read'],device:{id:deviceId,publicKey:pub.toString('base64url'),signature:crypto.sign(null,Buffer.from(signed),keys.privateKey).toString('base64url'),signedAt:now,nonce}}}));
    }
    if(message.type==='res' && message.id==='connect'){
      if(!message.ok)return finish(null,Error('connect rejected: '+JSON.stringify(message.error)));
      socket.send(JSON.stringify({type:'req',id:'health',method:'health',params:{}}));
    }
    if(message.type==='res' && message.id==='health'){
      if(!message.ok)return finish(null,Error('health RPC rejected: '+JSON.stringify(message.error)));
      if(diagnose){socket.send(JSON.stringify({type:'req',id:'channels',method:'channels.status',params:{probe:false}}));return;}
      socket.send(JSON.stringify({type:'req',id:'metadata',method:'chat.metadata',params:{agentId:'main'}}));
    }
    if(message.type==='res' && message.id==='channels'){
      const accounts=message.payload?.channelAccounts?.['redis-team'];
      console.log('DIAGNOSTIC_CHANNEL_STATUS',JSON.stringify({ok:message.ok,error:message.error,accounts:accounts&&Object.values(accounts).map(a=>Object.fromEntries(['accountId','enabled','configured','linked','running','connected','healthState','statusState','lastError','lifecycle','ingressUnavailable'].map(k=>[k,a[k]])))}));
      return finish(101);
    }
    if(message.type==='res' && message.id==='metadata'){
      if(!message.ok)return finish(null,Error('chat metadata RPC rejected: '+JSON.stringify(message.error)));
      socket.send(JSON.stringify({type:'req',id:'history',method:'chat.history',params:{sessionKey:'agent:main:redis-team:group:900',limit:10}}));
    }
    if(message.type==='res' && message.id==='history'){
      // 10.2 explicitly advertises short retryable access-facts hydration after
      // admission; retry only this native storage response, never auth failures.
      const retries=storageRetries.get('history')||0;
      if(!message.ok && coreVersion==='2026.10.2' && message.error?.code==='UNAVAILABLE' && message.error.retryable===true && Number.isFinite(message.error.retryAfterMs) && retries<8){
        storageRetries.set('history',retries+1);
        setTimeout(()=>{if(!done)socket.send(JSON.stringify({type:'req',id:'history',method:'chat.history',params:{sessionKey:'agent:main:redis-team:group:900',limit:10}}));},Math.min(1000,Math.max(250,message.error.retryAfterMs)));
        return;
      }
      finish(101,message.ok?null:Error('history RPC rejected: '+JSON.stringify(message.error)));
    }
  });
  socket.on('unexpected-response',(_,res)=>{res.resume();finish(res.statusCode);socket.terminate();});
  socket.on('error',error=>finish(null,error));
  socket.on('close',(code,reason)=>{if(!done)finish(null,Error(`WebSocket closed before complete RPC checks: ${code} ${reason}`));});
});}
const created=[];
let renewal;
try{
  for(let i=0;i<60;i++){try{await control('/v1/health');break;}catch{if(agent.exitCode!==null)throw Error(logs);await pause(250);}}
  await policy();renewal=setInterval(()=>policy().catch(()=>{}),15000);
  const roles=process.env.TEST_MANAGED_STARTUP_ROLES?.split(',')||['standalone','leader','developer','reviewer'];
  for(const [index,role] of roles.entries()){
    const id=900+index;
    const environment={CLAWMANAGER_INSTANCE_ID:String(id),CLAWMANAGER_OPENCLAW_VERSION:coreVersion,CLAWMANAGER_INSTANCE_TOKEN:'isolated-instance-'+id};
    // Match production idle members: real Redis consumer, empty inbox, no
    // bootstrap/heartbeat/model messages. autoRun=true never invents work.
    if(role!=='standalone')Object.assign(environment,{CLAWMANAGER_TEAM_ENABLED:'true',CLAWMANAGER_TEAM_ID:'900',CLAWMANAGER_TEAM_MEMBER_ID:role,CLAWMANAGER_TEAM_ROLE:role==='leader'?'leader':'worker',CLAWMANAGER_TEAM_REDIS_URL:'redis://proxy-startup-redis:6379/0',CLAWMANAGER_TEAM_AUTORUN:'true'});
    const req={instance_id:id,user_id:1,agent_type:'openclaw',workspace_path:path.join(root,'openclaw','user-1',`instance-${id}`),generation:1,uid:200000+id,gid:100,port_range:{start:20000,end:20200},environment};
    const result=await control('/v1/gateways','POST',req);created.push(result.gateway_id);
    let state;
    for(let i=0;i<180;i++){state=await control('/v1/gateways/'+result.gateway_id);if(state.state==='running')break;if(state.state==='error')throw Error(`${role}: ${state.error_message}`);if(i===100&&state.port){for(const route of ['startupz','readyz']){try{const r=await fetch(`http://127.0.0.1:${state.port}/${route}`);console.log('DIAGNOSTIC_ADMISSION',role,route,r.status,await r.text());}catch{}}await ws(state.port,id,ip,proxyIP,false,true);}await pause(500);}
    assert.equal(state.state,'running',`${role} not healthy`);
    const configFile=path.join(root,'openclaw','user-1',`instance-${id}`,'home','.openclaw','openclaw.json');
    const cfg=JSON.parse(fs.readFileSync(configFile,'utf8'));
    assert.equal(cfg.gateway.auth.mode,'trusted-proxy');
    assert.equal(cfg.gateway.trustedProxies,undefined,'fresh config must not depend on legacy CIDR');
    if(role!=='standalone'){
      assert.equal(cfg.channels['redis-team'].accounts.default.memberId,role);
      assert.equal(cfg.plugins.entries['redis-team'].enabled,true);
    }
    const before=fs.readFileSync(configFile,'utf8');
    assert.equal((await request(state.port,id)).status,200);
    assert.equal(await ws(state.port,id),101);
    assert.equal(await ws(state.port,id,'127.0.0.1'),403,'spoofed forwarded headers accepted');
    console.log(`PASS: real Agent fresh ${role}, per-instance UID, generated config, HTTP and WS, spoof rejection`);
    if(role==='standalone'){
      if(process.env.TEST_MANAGED_AUTOMATIONS==='1'){
        const cli=(args)=>{
          const r=spawnSync('openclaw',['automations',...args,'--json','--port',String(state.port)],{uid:req.uid,gid:req.gid,cwd:req.workspace_path,env:{...process.env,HOME:path.dirname(path.dirname(configFile)),OPENCLAW_STATE_DIR:path.dirname(configFile),OPENCLAW_CONFIG_PATH:configFile,OPENCLAW_GATEWAY_TOKEN:'',OPENCLAW_GATEWAY_PASSWORD:environment.CLAWMANAGER_INSTANCE_TOKEN},encoding:'utf8',timeout:40000});
          assert.equal(r.status,0,'native automation '+args[0]+': '+r.stderr+r.stdout);
          return JSON.parse(r.stdout);
        };
        const key='clawmanager:scheduled-task:isolated-disabled';
        const args=['add','--declaration-key',key,'--name','Isolated disabled fixture','--every','24h','--system-event','local fixture only','--session','main','--disabled'];
        cli(args);cli(args);
        const listed=cli(['list','--all']),rows=Array.isArray(listed)?listed:listed.jobs||listed.automations||listed.items;
        assert.ok(Array.isArray(rows),'native automation list has no supported records');
        const owned=rows.filter(x=>(x.declarationKey||x.declaration_key)===key);
        assert.equal(owned.length,1,'declaration retry created a duplicate');
        assert.equal(owned[0].enabled,false,'fixture must never execute');
        cli(['remove',owned[0].id]);
        const removed=cli(['list','--all']);
        assert.equal((Array.isArray(removed)?removed:removed.jobs||removed.automations||removed.items).some(x=>(x.declarationKey||x.declaration_key)===key),false);
        console.log('PASS: native automation add/list/remove and declaration idempotency; disabled, no model calls');
      }
      clearInterval(renewal);
      const priorSocket = await ws(state.port,id,ip,proxyIP,true);
      await policy();
      assert.equal(await ws(state.port,id),101,'same-address lease renewal must remain authenticated');
      assert.equal(priorSocket.readyState,WebSocket.OPEN,'renewal must not disconnect unchanged clients');
      const priorClosed = new Promise((resolve,reject)=>{
        const timer=setTimeout(()=>reject(Error('revoked existing client remained connected')),10000);
        priorSocket.once('close',(code)=>{clearTimeout(timer);resolve(code);});
      });
      await policy([rotatedProxyIP]);
      assert.equal(await ws(state.port,id),403,'old proxy must be revoked');
      assert.equal(await ws(state.port,id,ip,rotatedProxyIP),101,'new proxy must complete signed connect and health RPC');
      assert.equal(await priorClosed,4001,'revoked existing client must be closed using native auth rotation');
      await policy();
      assert.equal(await ws(state.port,id),101,'restored policy must authenticate again');
      clearInterval(renewal);await policy(['10.254.254.254']);
      assert.equal(await ws(state.port,id),403,'revoked proxy accepted');
      await policy();assert.equal(await ws(state.port,id),101);
      assert.equal(fs.readFileSync(configFile,'utf8'),before,'policy rotation changed config');
      // Restore the App's ongoing lease renewal before a restart; under build
      // load a valid 10.2 cold boot can outlast one policy lease. Expiry remains
      // fail-closed and is tested separately above, not disabled in production.
      renewal=setInterval(()=>policy().catch(()=>{}),15000);
      const again=await control('/v1/gateways','POST',req);assert.equal(again.gateway_id,result.gateway_id,'duplicate create changed writer');
      assert.equal((await control('/v1/gateways/'+result.gateway_id)).pid,state.pid,'policy rotation restarted Gateway');
      // Controlled local-only restart using the same workspace, no doctor or migration.
      await control('/v1/gateways/'+result.gateway_id,'DELETE');created.pop();
      await control('/v1/gateways','POST',req);created.push(result.gateway_id);
      for(let i=0;i<180;i++){state=await control('/v1/gateways/'+result.gateway_id);if(state.state==='running')break;if(state.state==='error')throw Error(state.error_message);await pause(500);}
      assert.equal(state.state,'running');assert.equal(await ws(state.port,id),101);
      console.log('PASS: existing workspace restart, idempotent create and hot proxy rotation without config rewrite');
    }
  }
  assert.ok(logs.includes('redis-team'),'Team plugin was not loaded');
  console.log('PASS: full managed startup fixture');
}catch(error){console.error(logs.split('\n').filter(line=>/redis-team|channel.*(fail|error|start)|plugin.*(fail|error)|admission.*(not ready|failed)/i.test(line)).join('\n').slice(-10000));console.error(logs.slice(-10000));throw error;}
finally{
  clearInterval(renewal);
  for(const id of created)await control('/v1/gateways/'+id,'DELETE').catch(()=>{});
  agent.kill('SIGTERM');await Promise.race([new Promise(r=>agent.once('exit',r)),pause(5000)]);
  if(agent.exitCode===null)agent.kill('SIGKILL');backend.close();
  // Container is disposable; leave its diagnostics until Docker removes it.
}
