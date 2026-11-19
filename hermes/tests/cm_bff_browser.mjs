// Fixed candidate artifact. This is an execution harness, not a replacement
// renderer, mock server, capability override, or arbitrary-command adapter.
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import https from 'node:https';
import http from 'node:http';
import { createHash, randomBytes, X509Certificate } from 'node:crypto';

const CASES = ['candidate_admission_identity','cm_build_renderer_resources','browser_login_ownership',
  'desktop_boot_original_dom','cm_cookie_scope','http_contract','browser_origin_rejection',
  'ws_single_use_identity','ui_prompt_stream_history','rpc_session_lifecycle_reconnect','interrupt',
  'approval_once','approval_deny','clarify','three_replica_tickets','lease_renewal_logout',
  'secret_surface_audit','stub_only_observation'];
const report = {schema_version:1,suite:'cm_bff_browser',status:'failed',category:'not_started',run_id:'',
  binding_sha256:'',cases:Object.fromEntries(CASES.map(x=>[x,'not_run'])),counts:{}};
class Failure extends Error { constructor(category){super(category);this.category=category;} }
const check = (ok, category) => { if(!ok) throw new Failure(category); };
const sha = raw => createHash('sha256').update(raw).digest('hex');
const sleep = ms => new Promise(resolve=>setTimeout(resolve,ms));
let activeCase='', browser, context, cfg, binding, ca, canaries=[], failureCount=0, networkCount=0;
let cmToken='', primaryCookie='', firstExpiry=0, admission, page, frame, responseTasks=[];
const packets=[], outgoing=[], wsHandshakes=[], resourceHashes=new Map();
let frameBytes=0, consoleCount=0, bootstrapCount=0, loginStatus=0;
function audit(value){const text=typeof value==='string'?value:JSON.stringify(value);if(canaries.some(s=>text.includes(s)))failureCount++;}
const object = value => value!==null&&typeof value==='object'&&!Array.isArray(value);
const uuid = value => typeof value==='string'&&/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value);
// This is the root runner's independently collected Kubernetes record. The
// admission response cannot invent Pod identities for the three-replica case.
export function readCMProvenance(bindingPath, expected, namespace){
  let fd;
  try{
    const filename=path.join(path.dirname(bindingPath),'cm-provenance.json'), before=fs.lstatSync(filename);
    check(before.isFile()&&!before.isSymbolicLink()&&before.size<=1048576,'cm_provenance_file_invalid');
    fd=fs.openSync(filename,fs.constants.O_RDONLY|(fs.constants.O_NOFOLLOW||0));
    const opened=fs.fstatSync(fd);check(opened.isFile()&&opened.dev===before.dev&&opened.ino===before.ino,'cm_provenance_file_changed');
    const buffer=Buffer.alloc(1048577);let size=0,n;
    while(size<buffer.length&&(n=fs.readSync(fd,buffer,size,buffer.length-size,null))>0)size+=n;
    const after=fs.lstatSync(filename), final=fs.fstatSync(fd);
    check(!after.isSymbolicLink()&&after.dev===opened.dev&&after.ino===opened.ino&&size<=1048576&&size===opened.size&&final.size===opened.size&&final.mtimeMs===opened.mtimeMs,'cm_provenance_file_changed');
    const raw=buffer.subarray(0,size);check(sha(raw)===expected.cm_provenance_sha256,'cm_provenance_hash_mismatch');
    const value=JSON.parse(raw);
    check(value.schema_version===1&&value.namespace===namespace&&object(value.cm)&&value.cm.image_digest===expected.cm_image_digest,'cm_provenance_identity_mismatch');
    const replicas=value.cm.replicas;
    check(Array.isArray(replicas)&&replicas.length===3&&new Set(replicas.map(r=>r?.uid)).size===3&&replicas.every(r=>object(r)&&uuid(r.uid)&&r.image_digest===expected.cm_image_digest),'cm_provenance_replicas_invalid');
    return value;
  }catch(error){if(error instanceof Failure)throw error;throw new Failure('cm_provenance_unreadable');}
  finally{if(fd!==undefined)fs.closeSync(fd);}
}
export function assertReplicaProvenance(provenance, replicaIds){
  check(Array.isArray(replicaIds)&&replicaIds.length===3&&new Set(replicaIds).size===3&&replicaIds.every(uuid),'three_real_replicas_required');
  const observed=new Set(provenance.cm.replicas.map(r=>r.uid));
  check(observed.size===3&&replicaIds.every(id=>observed.has(id)),'candidate_admission_replica_identity_mismatch');
}
export function assertHTTPContract(pathname,value){
  check(object(value)&&!Object.hasOwn(value,'error')&&value.success!==false&&value.ok!==false,'http_contract_error_response');
  const route=pathname.split('?')[0], text=x=>typeof x==='string', count=x=>Number.isInteger(x)&&x>=0;
  let valid=false;
  if(route==='/api/status')valid=text(value.version)&&count(value.active_sessions);
  else if(route==='/api/model/info')valid=text(value.model)&&text(value.provider)&&count(value.effective_context_length);
  else if(route==='/api/model/options')valid=text(value.model)&&text(value.provider)&&Array.isArray(value.providers)&&value.providers.every(p=>object(p)&&text(p.slug));
  else if(route==='/api/config'||route==='/api/config/defaults'){
    const sections={agent:['reasoning_effort','service_tier'],display:['personality','skin','interim_assistant_messages','timestamps'],terminal:['font_family'],voice:['max_recording_seconds']};
    valid=Object.entries(value).every(([section,fields])=>Object.hasOwn(sections,section)&&object(fields)&&Object.entries(fields).every(([name,v])=>sections[section].includes(name)&&(v===null||['string','number','boolean'].includes(typeof v))));
  }else if(route==='/api/sessions')valid=count(value.total)&&Array.isArray(value.sessions)&&value.sessions.every(s=>object(s)&&text(s.id));
  else if(route==='/api/profiles/sessions/sidebar')valid=['recents','cron','messaging'].every(k=>object(value[k])&&Array.isArray(value[k].sessions)&&object(value[k].profiles_truncated)&&typeof value[k].profiles_truncated.default==='boolean');
  else if(/^\/api\/sessions\/[^/]+\/messages$/.test(route))valid=text(value.session_id)&&Array.isArray(value.messages)&&value.messages.every(m=>object(m)&&text(m.role));
  check(valid,'http_contract_response_shape');return value;
}
async function waitFor(fn, category, timeout=60000){const until=Date.now()+timeout;while(Date.now()<until){const value=await fn();if(value)return value;await sleep(100);}throw new Failure(category);}
async function run(name, task){activeCase=name;await task();report.cases[name]='passed';activeCase='';}

// Node verifies the configured CA before Chrome starts. Chrome receives only
// the public-key pin of that verified leaf, never ignoreHTTPSErrors=true or a
// global certificate-verification bypass. All test requests disallow redirects.
async function request(url, {method='GET',headers={},body,upgrade=false}={}) {
  const target=new URL(url);check(['https:','http:'].includes(target.protocol),'request_scheme_invalid');
  if(target.protocol==='http:')check(['localhost','127.0.0.1','[::1]'].includes(target.hostname),'cleartext_target_forbidden');
  return new Promise((resolve,reject)=>{
    const transport=target.protocol==='https:'?https:http;
    const req=transport.request(target,{method,ca,headers,timeout:15000,rejectUnauthorized:true},res=>{
      const chunks=[];let size=0;
      res.on('data',chunk=>{size+=chunk.length;if(size>8*1024*1024){req.destroy();reject(new Failure('http_body_limit'));}else chunks.push(chunk);});
      res.on('end',()=>{const raw=Buffer.concat(chunks);audit(raw.toString());resolve({status:res.statusCode,headers:res.headers,raw});});
    });
    req.on('upgrade',(res,socket)=>{socket.destroy();resolve({status:res.statusCode,headers:res.headers,raw:Buffer.alloc(0)});});
    req.on('error',()=>reject(new Failure('http_or_tls_connection_failed')));
    req.on('timeout',()=>req.destroy(new Error('timeout')));
    req.end(body);
  });
}
const json = response => {try{return JSON.parse(response.raw);}catch{throw new Failure('invalid_json_response');}};
async function cm(pathname, options={}){return request(cfg.cm_origin+pathname,options);}
async function authenticated(pathname, options={}) {
  return cm(pathname,{...options,headers:{Origin:cfg.cm_origin,Authorization:'Bearer '+cmToken,...options.headers}});
}
const base = id => '/api/v1/instances/'+id+'/hermes-desktop';
function cookieHeader(cookies){return cookies.map(c=>c.name+'='+c.value).join('; ');}
async function scoped(pathname,options={}){return cm(base(cfg.instance_id)+pathname,{...options,headers:{Origin:cfg.cm_origin,Cookie:primaryCookie,...options.headers}});}
async function handshake(url, headers={}) {
  const u=new URL(url,cfg.cm_origin);check(u.origin.replace(/^ws/,'http')===cfg.cm_origin&&u.pathname===base(cfg.instance_id)+'/ws','ws_scope_invalid');
  u.protocol='https:';
  return request(u,{upgrade:true,headers:{Upgrade:'websocket',Connection:'Upgrade','Sec-WebSocket-Version':'13',
    'Sec-WebSocket-Key':randomBytes(16).toString('base64'),Origin:cfg.cm_origin,Cookie:primaryCookie,...headers}});
}
function safeReason(response){const reason=json(response).data?.reason;return ['feature_disabled','runtime_capability_unsupported','runtime_unavailable','instance_not_running','unsupported_instance','team_not_supported','ticket_store_unavailable'].includes(reason)?reason:'candidate_admission_unavailable';}
async function freshTicket(replica){const r=await scoped('/ws-ticket',{method:'POST',headers:replica?{'X-ClawManager-Acceptance-Replica':replica}:{}});check(r.status===200,'cm_ws_ticket_failed');if(replica)check(r.headers['x-clawmanager-replica-id']===replica,'replica_route_not_observed');const data=json(r).data;check(typeof data?.url==='string'&&Date.parse(data.expires_at)>Date.now()&&Date.parse(data.expires_at)<=Date.now()+31000,'cm_ticket_shape_or_expiry');return data.url;}

// A native WebSocket in the actual renderer realm, using only its real public
// bridge for connection tickets. Helpers do not replace any App/bridge method.
async function openRPC(){
  await frame.evaluate(async()=>{
    const ticket=await window.hermesDesktop.getGatewayWsUrl();
    if(ticket.ok!==true)throw new Error('ticket');
    const socket=new WebSocket(ticket.wsUrl), pending=new Map(),events=[],requests=[];let n=0;
    const state={socket,events,requests,closed:false,reply(id,result){socket.send(JSON.stringify({jsonrpc:'2.0',id,result}));},packet(method,params={}){return new Promise((resolve,reject)=>{
      const id='acceptance-'+(++n),timer=setTimeout(()=>{pending.delete(id);reject(new Error('rpc_timeout'));},45000);
      pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({jsonrpc:'2.0',id,method,params}));
    });}};
    socket.addEventListener('message',message=>{for(const line of String(message.data).split('\n').filter(Boolean)){
      let p;try{p=JSON.parse(line);}catch{socket.close();return;}
      if(p.id!==undefined&&pending.has(p.id)){const waiter=pending.get(p.id);pending.delete(p.id);clearTimeout(waiter.timer);waiter.resolve(p);}
      else if(p.method==='event'){if(events.length>=8192){socket.close();return;}events.push(p.params);}
      else if(p.id!==undefined&&['clarify','approval'].includes(p.method)){if(requests.length>=8192){socket.close();return;}requests.push(p);}
    }});
    socket.addEventListener('close',()=>{state.closed=true;for(const p of pending.values()){clearTimeout(p.timer);p.reject(new Error('closed'));}pending.clear();});
    await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',()=>reject(new Error('open')),{once:true});setTimeout(()=>reject(new Error('timeout')),15000);});
    window.__hermesAcceptance=state;
    const capability=await state.packet('client.capabilities',{server_requests:true});
    if(capability.error||!('result'in capability))throw new Error('server_request_capability');
  });
}
async function packet(method,params={}){const value=await frame.evaluate(({method,params})=>window.__hermesAcceptance.packet(method,params),{method,params});audit(value);return value;}
async function rpc(method,params={}){const p=await packet(method,params);check(p.jsonrpc==='2.0'&&'result'in p&&!p.error,'rpc_'+method.replaceAll('.','_')+'_failed');return p.result;}
async function events(){return frame.evaluate(()=>window.__hermesAcceptance.events);}
async function event(kind,sid,after=0){return waitFor(async()=>{const rows=await events();return rows.slice(after).find(e=>e.type===kind&&e.session_id===sid);},'event_'+kind.replaceAll('.','_')+'_timeout');}
async function serverRequests(){return frame.evaluate(()=>window.__hermesAcceptance.requests);}
async function serverRequest(method,sid,after=0){return waitFor(async()=>{const rows=await serverRequests();return rows.slice(after).find(p=>p.method===method&&p.params?.session_id===sid);},'server_request_timeout');}
async function replyRequest(id,result){return frame.evaluate(({id,result})=>window.__hermesAcceptance.reply(id,result),{id,result});}
async function bridgeAPI(pathname){const value=await frame.evaluate(path=>window.hermesDesktop.api({path}),pathname);audit(value);return assertHTTPContract(pathname,value);}
const marker = kind => 'ACCEPTANCE_'+binding.run_id+'_'+kind;
async function stubObservation(){const token=JSON.parse(fs.readFileSync(cfg.stub_observer_credentials_file)).token;const res=await request(cfg.stub_observation_url,{headers:{Authorization:'Bearer '+token}});check(res.status===200,'stub_observation_unavailable');const obj=json(res);check(obj.schema_version===1&&obj.run_id===binding.run_id&&obj.model_mode==='deterministic-stub'&&obj.external_requests===0,'stub_observation_identity_or_egress');return obj;}

async function main(){
  const args=process.argv.slice(2);check(args.length===4&&args[0]==='--config'&&args[2]==='--binding','driver_arguments_invalid');
  cfg=JSON.parse(fs.readFileSync(args[1]));const bindingRaw=fs.readFileSync(args[3]);binding=JSON.parse(bindingRaw);
  report.run_id=binding.run_id;report.binding_sha256=sha(bindingRaw);
  check(sha(fs.readFileSync(args[1]))===binding.execution_config_sha256,'driver_config_binding_changed');
  check(sha(fs.readFileSync(new URL(import.meta.url)))===binding.harness_sha256,'driver_harness_binding_changed');
  check(process.version===binding.node_version,'node_version_mismatch');
  const provenance=readCMProvenance(args[3],binding,cfg.namespace);
  report.identity=Object.fromEntries(['candidate_image_digest','payload_sha256','cm_image_digest','renderer_build_input_sha256','renderer_asset_manifest_sha256','harness_sha256'].map(k=>[k,binding[k]]));
  ca=fs.readFileSync(cfg.ca_file);canaries=JSON.parse(fs.readFileSync(cfg.secret_canaries_file)).values;
  const credentials=JSON.parse(fs.readFileSync(cfg.credentials_file));
  const spki=await new Promise((resolve,reject)=>{const req=https.get(cfg.cm_origin+'/healthz',{ca,rejectUnauthorized:true,timeout:15000},res=>{try{check(res.statusCode===200,'cm_health_failed');const cert=new X509Certificate(res.socket.getPeerCertificate().raw);res.resume();resolve(createHash('sha256').update(cert.publicKey.export({type:'spki',format:'der'})).digest('base64'));}catch{reject(new Failure('cm_health_or_certificate_failed'));}});req.on('error',()=>reject(new Failure('cm_ca_validation_failed')));req.on('timeout',()=>req.destroy());});
  const {chromium}=await import(pathToFileURL(path.join(cfg.runner.playwright_package_path,'index.mjs')).href);
  browser=await chromium.launch({executablePath:cfg.runner.browser_path,headless:true,args:['--ignore-certificate-errors-spki-list='+spki]});
  check(browser.version()===binding.browser_version,'browser_version_mismatch');
  context=await browser.newContext({ignoreHTTPSErrors:false,viewport:{width:1440,height:1000},locale:'en-US',serviceWorkers:'block'});
  // Abort unauthorized egress, never fulfill/mutate a server response.
  await context.route('**/*',route=>{const u=new URL(route.request().url());if(u.origin!==cfg.cm_origin){networkCount++;return route.abort('blockedbyclient');}return route.continue();});
  page=await context.newPage();
  await page.addInitScript(({instance})=>{
    window.__acceptanceObservedRendererReady=false;
    window.addEventListener('message',event=>{const target=document.querySelector('iframe[title$=" Hermes Desktop Web"]');
      if(target&&event.source===target.contentWindow&&event.origin===location.origin&&event.data?.instanceId===instance&&event.data?.type==='clawmanager:hermes-desktop:ready')window.__acceptanceObservedRendererReady=true;
    });
  },{instance:cfg.instance_id});
  page.on('console',message=>{consoleCount++;audit(message.text());});page.on('pageerror',error=>{consoleCount++;audit(String(error));});
  page.on('response',response=>{const u=new URL(response.url());if(u.origin!==cfg.cm_origin)return;
    if(u.pathname==='/api/v1/auth/login'&&response.request().method()==='POST')loginStatus=response.status();
    if(u.pathname===base(cfg.instance_id)+'/bootstrap'&&response.request().method()==='POST'&&response.status()===200)bootstrapCount++;
    if(u.pathname.startsWith('/hermes-desktop-web/')||u.pathname.startsWith(base(cfg.instance_id)+'/')){
      responseTasks.push((async()=>{try{const body=await response.body();check(body.length<=8*1024*1024,'browser_response_body_limit');audit(body.toString());if(u.pathname.startsWith('/hermes-desktop-web/'))resourceHashes.set(u.pathname,sha(body));}catch{if(response.status()!==101&&!(response.status()>=300&&response.status()<400))failureCount++;}})());
      if(responseTasks.length>2048)failureCount++;
    }
  });
  page.on('websocket',socket=>{
    const u=new URL(socket.url());if(u.origin.replace(/^ws/,'http')!==cfg.cm_origin||u.pathname!==base(cfg.instance_id)+'/ws'){networkCount++;return;}
    for(const [name,target]of[['framesent',outgoing],['framereceived',packets]])socket.on(name,evt=>{
      const text=String(evt.payload);frameBytes+=Buffer.byteLength(text);audit(text);if(frameBytes>32*1024*1024||target.length>8192){failureCount++;return;}
      for(const line of text.split('\n').filter(Boolean)){try{target.push(JSON.parse(line));}catch{failureCount++;}}
    });
  });
  const cdp=await context.newCDPSession(page);await cdp.send('Network.enable');
  cdp.on('Network.webSocketHandshakeResponseReceived',e=>wsHandshakes.push({status:e.response.status,replica:e.response.headers['X-ClawManager-Replica-ID']||e.response.headers['x-clawmanager-replica-id']}));
  await run('cm_build_renderer_resources',async()=>{
    const v=json(await cm('/api/v1/version')).data;for(const k of ['version','commit','build_time'])check(v?.[k]===cfg.cm_identity[k],'cm_build_identity_mismatch');
    const info=json(await cm('/hermes-desktop-web/build-info.json'));
    check(info.renderer_entry==='apps/desktop/src/main.tsx'&&info.acceptance==='not-asserted-by-build'&&info.hermes_commit==='bf0203a794a07aac3ff764972ab9087baa4e4f4e'&&info.build_input_sha256===binding.renderer_build_input_sha256,'renderer_identity_mismatch');
    const manifestRaw=fs.readFileSync(cfg.renderer_asset_manifest_file);check(sha(manifestRaw)===binding.renderer_asset_manifest_sha256,'renderer_manifest_binding_mismatch');
    const manifest=JSON.parse(manifestRaw), entries=Object.entries(manifest);check(entries.length>=3&&entries.length<=1024,'renderer_manifest_shape');
    let cursor=0;await Promise.all(Array.from({length:6},async()=>{while(cursor<entries.length){const[p,h]=entries[cursor++];check(/^[A-Za-z0-9_.\/-]+$/.test(p)&&!p.startsWith('/')&&!p.split('/').some(x=>!x||x==='.'||x==='..')&&/^[a-f0-9]{64}$/.test(h),'renderer_manifest_entry');const r=await cm('/hermes-desktop-web/'+p);check(r.status===200&&sha(r.raw)===h,'renderer_resource_hash_mismatch');}}));
    report.counts.renderer_resources=entries.length;
  });
  await run('browser_login_ownership',async()=>{
    await page.goto(cfg.cm_origin+'/login',{waitUntil:'domcontentloaded'});
    await page.locator('#username').fill(credentials.username);await page.locator('#password').fill(credentials.password);
    await page.locator('button[type="submit"]').click();
    try{await page.waitForURL(cfg.cm_origin+'/dashboard',{timeout:30000});}catch{throw new Failure(loginStatus===401?'cm_test_credentials_rejected':loginStatus===429?'cm_test_login_rate_limited':'cm_browser_login_failed');}
    cmToken=await page.evaluate(()=>localStorage.getItem('access_token'));check(typeof cmToken==='string'&&cmToken.length>20,'cm_login_token_missing');
    const me=json(await authenticated('/api/v1/auth/me')).data;check(Number.isInteger(me?.id)&&me.is_active!==false&&me.role==='user','isolated_nonadmin_principal_required');
    report.counts.principal_user_id=me.id;
    const denied=await authenticated(base(cfg.other_instance_id)+'/bootstrap');check([403,404].includes(denied.status),'other_instance_ownership_not_denied');
  });
  await run('candidate_admission_identity',async()=>{
    const r=await authenticated('/api/v1/hermes-desktop-test/campaigns/'+binding.run_id+'/observation');
    check(r.status===200,'isolated_candidate_admission_unavailable');admission=json(r);if(admission.data)admission=admission.data;
    check(admission.schema_version===1&&admission.scope==='isolated-candidate'&&admission.run_id===binding.run_id&&admission.namespace===cfg.namespace,'candidate_admission_scope_mismatch');
    check(admission.cm_image_digest===binding.cm_image_digest&&admission.runtime_image_digest===binding.candidate_manifest_digest&&admission.runtime_payload_sha256===binding.payload_sha256&&admission.renderer_build_input_sha256===binding.renderer_build_input_sha256,'candidate_admission_identity_mismatch');
    check(admission.runtime_accepted===false&&admission.production_gate_unchanged===true&&admission.model_mode==='deterministic-stub'&&admission.principal_user_id===report.counts.principal_user_id,'candidate_admission_policy_mismatch');
    check(Array.isArray(admission.instance_ids)&&admission.instance_ids.length===2&&[cfg.instance_id,cfg.other_instance_id].every(id=>admission.instance_ids.includes(id)),'candidate_admission_instances_mismatch');
    check(Date.parse(admission.expires_at)>Date.now()+16*60000&&Date.parse(admission.expires_at)<Date.now()+2*3600000,'candidate_admission_expiry_invalid');
    assertReplicaProvenance(provenance,admission.replica_ids);
    await stubObservation();
    const descriptor=await authenticated(base(cfg.instance_id)+'/bootstrap');check(descriptor.status===200,'candidate_bootstrap_failed');check(json(descriptor).data?.available===true,safeReason(descriptor));
  });
  await run('desktop_boot_original_dom',async()=>{
    await page.goto(cfg.cm_origin+'/instances/'+cfg.instance_id,{waitUntil:'domcontentloaded'});
    const iframe=page.locator('iframe[title$=" Hermes Desktop Web"]');await iframe.waitFor({state:'visible',timeout:60000});
    check(new URL(await iframe.getAttribute('src'),cfg.cm_origin).href===cfg.cm_origin+'/hermes-desktop-web/?instance_id='+cfg.instance_id,'renderer_frame_scope');
    frame=await(await iframe.elementHandle()).contentFrame();check(frame,'renderer_frame_missing');
    await frame.waitForFunction(()=>typeof window.hermesDesktop?.getGatewayWsUrl==='function',{},{timeout:60000});
    const editor=frame.locator('[data-slot="composer-rich-input"][contenteditable="true"]');await editor.waitFor({state:'visible',timeout:60000});
    await page.waitForFunction(()=>window.__acceptanceObservedRendererReady===true,{},{timeout:60000});
    check(await editor.count()===1,'original_composer_missing_or_ambiguous');
    check(await frame.locator('#root').textContent(),'renderer_root_empty');
    check(await frame.locator('input[type="password"]').count()===0,'runtime_second_login_visible');
    await waitFor(()=>packets.some(p=>p.result?.session_id)||outgoing.some(p=>p.method==='setup.status'||p.method==='gateway.ping'||p.method==='ping'),'original_renderer_rpc_not_observed');
    report.counts.original_composer=1;
  });
  await run('cm_cookie_scope',async()=>{
    const cookies=await context.cookies();const current=cookies.filter(c=>c.name==='cm_hermes_desktop_'+cfg.instance_id);
    check(current.length===1&&current[0].httpOnly&&current[0].secure&&current[0].sameSite==='Strict'&&current[0].path===base(cfg.instance_id)+'/','cm_cookie_security_attributes');
    check(current[0].expires*1000>Date.now()&&current[0].expires*1000<Date.now()+601000,'cm_cookie_lifetime');
    check(cookies.every(c=>c.name==='cm_hermes_desktop_'+cfg.instance_id),'unexpected_browser_cookie');
    primaryCookie=cookieHeader(current);firstExpiry=current[0].expires*1000;
    check(!await frame.evaluate(()=>document.cookie.includes('cm_hermes_desktop_')),'httponly_cookie_script_visible');
    const crossed=await cm(base(cfg.other_instance_id)+'/session',{headers:{Origin:cfg.cm_origin,Cookie:primaryCookie}});check([401,403].includes(crossed.status),'cross_instance_cookie_accepted');
  });
  await run('http_contract',async()=>{
    for(const p of ['/api/status','/api/model/info','/api/config','/api/config/defaults','/api/model/options?explicit_only=true','/api/sessions?limit=10','/api/profiles/sessions/sidebar?recents_profile=all&recents_limit=10'])await bridgeAPI(p);
    const bridge=await frame.evaluate(async()=>{const b=window.hermesDesktop,connection=await b.getConnection();let profileDenied=false,nativeDenied=false;
      try{await b.getConnection('other-profile');}catch{profileDenied=true;}
      try{await b.readFileText('/__acceptance_nonexistent__');}catch(error){nativeDenied=error?.code==='desktop_browser_unsupported';}
      return{connection,profileDenied,nativeDenied};});audit(bridge);
    check(bridge.connection?.token===''&&bridge.connection?.wsUrl===''&&bridge.connection?.connectionId==='clawmanager:'+cfg.instance_id&&bridge.profileDenied&&bridge.nativeDenied,'browser_bridge_scope_or_native_boundary');
    for(const p of ['/api/config/raw','/api/env','/api/fs/read-text?path=.hermes/.env']){const r=await scoped(p);check([403,404].includes(r.status),'sensitive_http_route_accepted');}
    const unauth=await cm(base(cfg.instance_id)+'/session',{headers:{Origin:cfg.cm_origin}});check(unauth.status===401,'unauthenticated_bff_accepted');
  });
  await run('browser_origin_rejection',async()=>{
    for(const origin of ['', 'https://wrong-origin.invalid']){const r=await scoped('/ws-ticket',{method:'POST',headers:{Origin:origin}});check(r.status===403,'incorrect_origin_http_accepted');}
    const ticket=await freshTicket();for(const origin of ['', 'https://wrong-origin.invalid']){const r=await handshake(ticket,{Origin:origin});check(r.status===403,'incorrect_origin_ws_accepted');}
  });
  await run('ws_single_use_identity',async()=>{
    const ticket=await freshTicket();check((await handshake(ticket,{Cookie:''})).status===401,'ticket_without_cookie_accepted');
    check((await handshake(ticket)).status===101,'valid_ticket_ws_rejected');check((await handshake(ticket)).status===401,'ticket_replay_accepted');
    await openRPC();check((await rpc('gateway.ping'))!==undefined,'bridge_rpc_ping_failed');
    check((await packet('shell.exec',{command:'echo acceptance-must-not-run'})).error,'native_rpc_accepted');
  });
  let sid,stored,history;
  await run('ui_prompt_stream_history',async()=>{
    const start=packets.length,sent=outgoing.length;const editor=frame.locator('[data-slot="composer-rich-input"][contenteditable="true"]');
    await editor.fill(marker('TEXT'));await editor.press('Enter');
    const submit=await waitFor(()=>outgoing.slice(sent).find(p=>p.method==='prompt.submit'&&p.params?.text===marker('TEXT')),'ui_prompt_not_sent');sid=submit.params.session_id;
    const completed=await waitFor(()=>packets.slice(start).find(p=>p.method==='event'&&p.params?.type==='message.complete'&&p.params.session_id===sid),'ui_stream_completion_missing');
    check(completed.params.payload?.status!=='error'&&packets.slice(start).some(p=>p.params?.type==='message.delta'&&p.params.session_id===sid),'ui_stream_delta_missing');
    await frame.getByText('Local deterministic response '+marker('TEXT'),{exact:false}).first().waitFor({state:'visible',timeout:30000});
    const created=packets.find(p=>p.result?.session_id===sid&&p.result?.stored_session_id);check(created,'ui_durable_session_identity_missing');stored=created.result.stored_session_id;
    const resumed=await rpc('session.resume',{session_id:stored,source:'web',defer_history:true});check(resumed.session_id===sid,'ui_session_resume_identity');
    history=await rpc('session.history',{session_id:sid});check(history.count>=2&&JSON.stringify(history).includes(marker('TEXT')),'ui_history_missing');
    const rest=await bridgeAPI('/api/sessions/'+stored+'/messages?limit=120&order=latest&include_compacted=true');check(JSON.stringify(rest).includes(marker('TEXT')),'ui_http_history_missing');
    report.counts.ui_prompt_submissions=1;
  });
  await run('rpc_session_lifecycle_reconnect',async()=>{
    await rpc('session.status',{session_id:sid});await rpc('session.usage',{session_id:sid});
    await frame.evaluate(()=>window.__hermesAcceptance.socket.close());await openRPC();
    const resumed=await rpc('session.resume',{session_id:stored,source:'web',omit_messages:true});check(resumed.session_id===sid,'reconnect_live_identity_changed');
    await rpc('session.activate',{session_id:sid,cols:96,omit_messages:true});
    check(JSON.stringify(await rpc('session.history',{session_id:sid}))===JSON.stringify(history),'reconnect_changed_history');
    const replay=await rpc('session.events.since',{session_id:sid,last_seen:0});check(Array.isArray(replay.events)&&replay.events.length>0,'reconnect_events_missing');
    const seq=replay.events.map(e=>e.seq);check(seq.every((n,i)=>Number.isInteger(n)&&(!i||n>seq[i-1])),'reconnect_events_not_ordered');
  });
  await run('interrupt',async()=>{const offset=(await events()).length;await rpc('prompt.submit',{session_id:sid,text:marker('SLOW')});await event('message.delta',sid,offset);await rpc('session.interrupt',{session_id:sid});const complete=await event('message.complete',sid,offset);check(complete.payload?.status==='interrupted','interrupt_not_observed');});
  for(const[name,kind,choice]of[['approval_once','ONCE','once'],['approval_deny','DENY','deny'],['clarify','CLARIFY','alpha']])await run(name,async()=>{
    const offset=(await events()).length,requestOffset=(await serverRequests()).length;await rpc('prompt.submit',{session_id:sid,text:marker(kind)});
    const req=await serverRequest(kind==='CLARIFY'?'clarify':'approval',sid,requestOffset),p=req.params;
    check(typeof req.id==='string','interactive_request_id_missing');
    if(kind==='CLARIFY'){check(Array.isArray(p.questions)&&p.questions.length>0,'clarify_questions_missing');await replyRequest(req.id,{answers:Object.fromEntries(p.questions.map(q=>[q.qid,choice])),outcome:'submitted'});}
    else{check(Array.isArray(p.choices)&&p.choices.includes(choice)&&p.choices.includes('deny'),'approval_choices_missing');await replyRequest(req.id,{choice});}
    const complete=await event('message.complete',sid,offset);check(complete.payload?.status!=='error','interactive_completion_failed');
  });
  await run('three_replica_tickets',async()=>{
    const ids=admission.replica_ids, ticket=await freshTicket(ids[0]);
    const first=await handshake(ticket,{'X-ClawManager-Acceptance-Replica':ids[1]});check(first.status===101&&first.headers['x-clawmanager-replica-id']===ids[1],'cross_replica_ticket_not_accepted');
    const replay=await handshake(ticket,{'X-ClawManager-Acceptance-Replica':ids[2]});check(replay.status===401&&replay.headers['x-clawmanager-replica-id']===ids[2],'cross_replica_replay_not_rejected');
    const race=await freshTicket(ids[0]),results=await Promise.all(ids.map(id=>handshake(race,{'X-ClawManager-Acceptance-Replica':id})));
    check(results.every((r,i)=>r.headers['x-clawmanager-replica-id']===ids[i])&&results.filter(r=>r.status===101).length===1&&results.filter(r=>r.status===401).length===2,'cross_replica_atomic_consume_failed');report.counts.replica_count=3;
  });
  await run('lease_renewal_logout',async()=>{
    const before=await rpc('session.history',{session_id:sid});
    // Let the real parent renew normally; retain the old socket and verify it
    // expires even though the browser cookie has already been refreshed.
    await waitFor(async()=>bootstrapCount>=2&&(await context.cookies()).some(c=>c.name==='cm_hermes_desktop_'+cfg.instance_id&&c.expires*1000>firstExpiry+1000),'parent_cookie_renewal_missing',Math.max(1000,firstExpiry-Date.now()+20000));
    await waitFor(()=>frame.evaluate(()=>window.__hermesAcceptance.closed),'old_websocket_lease_not_expired',Math.max(1000,firstExpiry-Date.now()+25000));
    primaryCookie=cookieHeader((await context.cookies()).filter(c=>c.name==='cm_hermes_desktop_'+cfg.instance_id));
    await openRPC();await rpc('session.resume',{session_id:stored,source:'web',defer_history:true});check(JSON.stringify(await rpc('session.history',{session_id:sid}))===JSON.stringify(before),'lease_reconnect_history_changed');
    const logout=await authenticated('/api/v1/auth/logout',{method:'POST'});check(logout.status===200,'cm_logout_failed');
    for(const id of admission.replica_ids){const r=await scoped('/session',{headers:{'X-ClawManager-Acceptance-Replica':id}});check(r.status===401&&r.headers['x-clawmanager-replica-id']===id,'logout_not_revoked_on_all_replicas');}
    await waitFor(()=>frame.evaluate(()=>window.__hermesAcceptance.closed),'logout_live_socket_not_closed',20000);
  });
  await run('secret_surface_audit',async()=>{
    await Promise.allSettled(responseTasks);audit(await frame.locator('body').innerText());
    const storage=await frame.evaluate(()=>{const dump=s=>Object.fromEntries(Array.from({length:s.length},(_,i)=>s.key(i)).filter(k=>k!==null).map(k=>[k,s.getItem(k)]));return{local:dump(localStorage),session:dump(sessionStorage),cookie:document.cookie};});audit(storage);
    check(!JSON.stringify(storage).includes(cmToken)&&!Object.hasOwn(storage.local,'access_token'),'manager_credentials_in_renderer_storage');
    check(failureCount===0,'runtime_secret_or_protocol_leak');check(networkCount===0,'unexpected_browser_origin');
    check(resourceHashes.size>=3&&wsHandshakes.some(x=>x.status===101),'browser_network_evidence_missing');
    report.counts.secret_canaries=canaries.length;report.counts.secret_matches=failureCount;report.counts.browser_console_events=consoleCount;report.counts.browser_websockets=wsHandshakes.length;
  });
  await run('stub_only_observation',async()=>{
    const observation=await stubObservation();check(Number.isInteger(observation.request_count)&&observation.request_count>=5,'stub_requests_missing');
    check(['TEXT','SLOW','ONCE','DENY','CLARIFY'].every(k=>observation.prompt_markers?.includes(marker(k))),'stub_prompt_markers_missing');
    check(Array.isArray(observation.instances)&&observation.instances.includes(cfg.instance_id)&&observation.instances.every(id=>[cfg.instance_id,cfg.other_instance_id].includes(id)),'stub_instance_binding_failed');
    check(observation.tool_counts?.terminal>0&&observation.tool_counts?.clarify>0,'stub_requested_tools_missing');
    check(observation.tool_schema_counts?.terminal>0&&observation.tool_schema_counts?.clarify>0,'stub_actual_tool_schemas_missing');
    const native=['read_terminal','close_terminal','desktop_preview','drive_preview','annotate_preview','read_window_below','focus_pane','react_to_message','setup_mcp','tour','tip','desktop_project','computer_use'];check(native.every(n=>!observation.tool_schema_counts?.[n]),'stub_native_tools_registered');
    check(Number.isInteger(observation.tool_results_observed)&&observation.tool_results_observed>=3,'stub_tool_feedback_missing');
    report.counts.model_stub_requests=observation.request_count;report.counts.external_model_requests=observation.external_requests;report.counts.tool_results_observed=observation.tool_results_observed;
  });
  check(Object.values(report.cases).every(x=>x==='passed'),'required_case_not_executed');report.status='passed';report.category='all_cases_passed';
}

let finishing=false,watchdog;
async function finish(){if(finishing)return;finishing=true;clearTimeout(watchdog);
  let cleanupTimer;
  try{await Promise.race([(async()=>{await context?.close();await browser?.close();})(),new Promise(resolve=>{cleanupTimer=setTimeout(resolve,10000);cleanupTimer.unref();})]);}catch{}finally{clearTimeout(cleanupTimer);}
  process.stdout.write(JSON.stringify(report)+'\n');process.exitCode=report.status==='passed'?0:1;
}
// The owned browser/profile is closed before the outer Python process timeout.
// No existing browser, CDP endpoint or user profile is attached to this run.
if(process.argv[1]&&pathToFileURL(fs.realpathSync(process.argv[1])).href===import.meta.url){
  watchdog=setTimeout(async()=>{report.status='failed';report.category='browser_total_deadline';if(activeCase)report.cases[activeCase]='failed';await finish();process.exit(1);},1400000);
  watchdog.unref();
  for(const signal of ['SIGTERM','SIGINT'])process.once(signal,async()=>{report.status='failed';report.category='browser_interrupted';if(activeCase)report.cases[activeCase]='failed';await finish();process.exit(1);});
  try{await main();}catch(error){report.category=error instanceof Failure?error.category:(activeCase?activeCase+'_execution_failed':'browser_execution_failed');if(activeCase)report.cases[activeCase]='failed';}
  finally{await finish();}
}
