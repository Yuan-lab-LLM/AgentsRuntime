// Release acceptance is a code/release decision, never an environment override.
// Keep false until the native task lifecycle has passed release acceptance.
export const capabilityRenewalReleaseAccepted = false;
const required = ['task_identity','turn_cancel','cancel_receipt','live_catalog'];
const terminal = new Set(['IDENTITY_CHANGED','AUTHORIZATION_DENIED','CONTRACT_CHANGED','RENEWAL_UNSUPPORTED','ENROLLMENT_REQUIRED','ENROLLMENT_UNSUPPORTED','INVALID_SERVER_CONTRACT']);
const fail = code => {const error=new Error(code);error.code=code;throw error;};

export function createCapabilityRenewal({
  enabled=false,releaseAccepted=false,baseURL,apiKey,report,
  allowEnrollment=false,canEnroll=()=>false,
  fetcher=globalThis.fetch,isCurrent=()=>true,onStatus=()=>{},
  scheduler={setTimeout:(fn,ms)=>setTimeout(fn,ms),clearTimeout:id=>clearTimeout(id)},
  timeoutMs=2500,retryMs=1000,retryMaxMs=15000,
}={}) {
  let state=enabled?(releaseAccepted?'idle':'release_blocked'):'disabled';
  let timer,requestAbort,pending,stopped=false,preflightPassed=false,failures=0,attempts=0,lastSuccessAt=null,lastErrorCode=null;
  let base,body,ttl=30;
  let enrollmentBody;
  if(enabled&&releaseAccepted) {
    if(typeof allowEnrollment!=='boolean'||typeof canEnroll!=='function')fail('INVALID_CONFIGURATION');
    let url;try{url=new URL(baseURL);}catch{fail('INVALID_CONFIGURATION');}
    if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash||
      typeof apiKey!=='string'||!apiKey.trim()||/[\r\n]/.test(apiKey)||
      !report||report.protocol!==1||!Number.isSafeInteger(report.generation)||report.generation<0||report.generation>2147483647||
      [report.runtime_version,report.adapter_version].some(v=>typeof v!=='string'||!v||v.trim()!==v||v.length>100||/[\r\n\0]/.test(v))||
      !Array.isArray(report.capabilities)||report.capabilities.length!==required.length||!required.every(c=>report.capabilities.includes(c))||
      ![timeoutMs,retryMs,retryMaxMs].every(v=>Number.isSafeInteger(v)&&v>0&&v<=60000)||retryMaxMs<retryMs)fail('INVALID_CONFIGURATION');
    base=url.href.replace(/\/+$/,'');
    // Snapshot only the wire contract; caller mutations cannot change identity.
    body=JSON.stringify({protocol:1,generation:report.generation,runtime_version:report.runtime_version,adapter_version:report.adapter_version,capabilities:[...report.capabilities]});
    report=JSON.parse(body);
  }
  const status=()=>({state,inFlight:!!pending,attempts,consecutiveFailures:failures,lastSuccessAt,lastErrorCode});
  const notify=()=>{try{onStatus(status());}catch{}};
  const current=()=>{let matches=false;try{matches=isCurrent()===true;}catch{}if(!matches)fail('IDENTITY_CHANGED');if(stopped)fail('STOPPED');};
  function stop(code='STOPPED') {
    stopped=true;state='stopped';lastErrorCode=code;
    if(timer!==undefined){scheduler.clearTimeout(timer);timer=undefined;}
    requestAbort?.abort();notify();
  }
  const checkTTL=value=>{if(!Number.isSafeInteger(value)||value<1||value>3600)fail('INVALID_SERVER_CONTRACT');return value;};
  async function request(endpoint,method,requestBody=body) {
    current();requestAbort=new AbortController();
    const signal=AbortSignal.any([requestAbort.signal,AbortSignal.timeout(timeoutMs)]);
    let reader;
    try {
      const response=await fetcher(base+endpoint,{method,redirect:'error',signal,
        headers:{Authorization:'Bearer '+apiKey,Accept:'application/json',...(method==='POST'?{'Content-Type':'application/json'}:{})},
        ...(method==='POST'?{body:requestBody}:{})});
      reader=response.body?.getReader();
      current();signal.throwIfAborted();
      if(response.status===401||response.status===403)fail('AUTHORIZATION_DENIED');
      if(response.status===409)fail('CONTRACT_CHANGED');
      if(response.status===400)fail('INVALID_SERVER_CONTRACT');
      if(response.status===404||response.status===405)fail('RENEWAL_UNSUPPORTED');
      if(!response.ok||!response.body)fail('RENEWAL_UNAVAILABLE');
      const chunks=[];let size=0;
      for(;;){const part=await reader.read();signal.throwIfAborted();current();if(part.done)break;
        size+=part.value.byteLength;if(size>8192)fail('INVALID_SERVER_CONTRACT');chunks.push(Buffer.from(part.value));}
      let value;try{value=JSON.parse(Buffer.concat(chunks).toString('utf8'));}catch{fail('INVALID_SERVER_CONTRACT');}
      if(!value||typeof value!=='object'||Array.isArray(value))fail('INVALID_SERVER_CONTRACT');
      return value;
    }finally{void reader?.cancel().catch(()=>{});requestAbort=undefined;}
  }
  function schedule(delay) {
    if(stopped)return;
    timer=scheduler.setTimeout(()=>{timer=undefined;return run();},delay);timer?.unref?.();
  }
  async function cycle() {
    state='reporting';attempts++;notify();
    try {
      current();
      if(!preflightPassed&&!enrollmentBody) {
        const value=await request('/runtime/capabilities','GET');
        if(value.protocol!==1||value.generation!==report.generation||value.registration_is_sticky!==true||
          !Array.isArray(value.required_capabilities)||value.required_capabilities.length!==required.length||
          new Set(value.required_capabilities).size!==required.length||!required.every(c=>value.required_capabilities.includes(c)))fail('INVALID_SERVER_CONTRACT');
        if(value.renewal_supported!==true)fail('RENEWAL_UNSUPPORTED');
        ttl=checkTTL(value.report_ttl_seconds);
        if(value.enrolled===true&&value.enrollment_generation===report.generation) {
          preflightPassed=true;
        } else {
          if(!allowEnrollment)fail('ENROLLMENT_REQUIRED');
          if(value.conditional_enrollment_supported!==true)fail('ENROLLMENT_UNSUPPORTED');
          const prior=value.enrollment_generation,revision=value.enrollment_revision;
          if(typeof value.enrolled!=='boolean'||(value.enrolled?
            !Number.isSafeInteger(prior)||prior<0||prior>=report.generation||typeof revision!=='string'||!/^[a-f0-9]{64}$/.test(revision):
            prior!==null||revision!==null))fail('INVALID_SERVER_CONTRACT');
          // Preserve the first inspected precondition across lost responses.
          // Never re-inspect and silently adopt a newer conflicting contract.
          enrollmentBody=JSON.stringify({report,expected_enrollment_revision:revision});
        }
      }
      if(enrollmentBody) {
        let ready=false;try{ready=canEnroll()===true;}catch{}
        if(!ready)fail('ENROLLMENT_NOT_READY');
        const value=await request('/runtime/capabilities/enroll','POST',enrollmentBody);
        if(value.protocol!==1||value.generation!==report.generation||value.enrolled!==true||value.task_identity_required!==true)fail('INVALID_SERVER_CONTRACT');
        ttl=checkTTL(value.report_ttl_seconds);
        enrollmentBody=undefined;preflightPassed=true;
      }
      const value=await request('/runtime/capabilities/renew','POST');
      current();
      if(value.protocol!==1||value.generation!==report.generation||value.renewed!==true||value.task_identity_required!==true)fail('INVALID_SERVER_CONTRACT');
      ttl=checkTTL(value.report_ttl_seconds);
      state='active';failures=0;lastErrorCode=null;lastSuccessAt=Date.now();notify();
      schedule(Math.max(100,Math.min(10000,Math.floor(ttl*1000/3))));
    }catch(error) {
      if(stopped)return;
      const code=terminal.has(error?.code)||error?.code==='ENROLLMENT_NOT_READY'?error.code:'RENEWAL_UNAVAILABLE';
      if(terminal.has(code)){stop(code);return;}
      state='retrying';lastErrorCode=code;failures++;notify();
      schedule(Math.min(retryMaxMs,retryMs*2**Math.min(failures-1,16)));
    }
  }
  function run() {
    if(!enabled||!releaseAccepted||stopped)return Promise.resolve();
    if(pending)return pending;
    pending=cycle().finally(()=>{pending=undefined;});
    return pending;
  }
  return Object.freeze({start:()=>{if(timer!==undefined)return Promise.resolve();return run();},stop,status});
}
