// Opt-in disposable fixture diagnostic; no remote server or model access.
const pid=Number(process.argv[2]);
if(!Number.isInteger(pid)||pid<=1)throw Error('explicit local Doctor PID required');
process.kill(pid,'SIGUSR1');
await new Promise(r=>setTimeout(r,1000));
const targets=await(await fetch('http://127.0.0.1:9229/json/list',{signal:AbortSignal.timeout(5000)})).json();
const socket=new WebSocket(targets[0].webSocketDebuggerUrl);
await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
let id=0;const pending=new Map();
socket.addEventListener('message',event=>{const msg=JSON.parse(event.data);if(pending.has(msg.id)){const [resolve,reject]=pending.get(msg.id);pending.delete(msg.id);msg.error?reject(Error(msg.error.message)):resolve(msg.result);}});
const rpc=method=>new Promise((resolve,reject)=>{const n=++id;pending.set(n,[resolve,reject]);socket.send(JSON.stringify({id:n,method}));});
try{
 await rpc('Profiler.enable');await rpc('Profiler.start');await new Promise(r=>setTimeout(r,3000));
 const {profile}=await rpc('Profiler.stop');
 const counts=new Map();for(const sample of profile.samples||[])counts.set(sample,(counts.get(sample)||0)+1);
 console.log(profile.nodes.map(n=>({name:n.callFrame.functionName,file:n.callFrame.url.split('/').slice(-1)[0],samples:counts.get(n.id)||0})).sort((a,b)=>b.samples-a.samples).slice(0,12));
}finally{socket.close();}
