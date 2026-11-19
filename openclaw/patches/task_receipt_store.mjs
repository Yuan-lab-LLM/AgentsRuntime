import fs from 'node:fs';
import path from 'node:path';
import {createHash,createHmac,timingSafeEqual,randomUUID} from 'node:crypto';
const hash=value=>createHash('sha256').update(value).digest('hex');
const validID=value=>typeof value==='string'&&/^[\x21-\x7e]{1,191}$/.test(value);
// Only settled receipts are stored. A crash during execution is NOT proof that
// native tools stopped and must never produce a cancellation acknowledgement.
export function createTaskReceiptStore({directory,baseURL,apiKey,generation,limit=128}) {
 if(!path.isAbsolute(directory)||!apiKey||!Number.isSafeInteger(generation)||generation<0||!Number.isSafeInteger(limit)||limit<1)throw new Error('Invalid receipt store configuration');
 fs.mkdirSync(directory,{recursive:true,mode:0o700});
 if(!fs.lstatSync(directory).isDirectory()||fs.lstatSync(directory).isSymbolicLink())throw new Error('Unsafe receipt directory');
 const namespace=hash(JSON.stringify([baseURL,apiKey,generation]));
 const root=path.join(directory,namespace);
 fs.mkdirSync(root,{mode:0o700,recursive:true});
 if(!fs.lstatSync(root).isDirectory()||fs.lstatSync(root).isSymbolicLink())throw new Error('Unsafe receipt namespace');
 // Execution barriers are instance-wide, not credential/generation scoped.
 // Their presence is uncertainty, NEVER evidence of completion/cancellation.
 const executionRoot=path.join(directory,'z-task-executions'),ownedExecutions=new Map();
 function assertNotMaintained(){
  try{fs.lstatSync(path.join(directory,'.task-maintenance.lock'));}catch(error){if(error.code==='ENOENT')return;throw error;}
  // Any entry (including a corrupt file or link) fails closed. An orphan lock
  // is never deleted automatically: maintenance may have stopped mid-archive.
  throw new Error('Receipt maintenance in progress');
 }
 function syncDirectory(dir){
  // Windows cannot fsync directory handles through Node. Production Linux
  // requires this barrier; do not silently ignore unsupported filesystem errors.
  if(process.platform==='win32')return;
  const fd=fs.openSync(dir,fs.constants.O_RDONLY|(fs.constants.O_DIRECTORY??0)|(fs.constants.O_NOFOLLOW??0));
  try{fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
 }
 syncDirectory(directory);
 function executionDirectory(create=false){
  if(create){try{fs.mkdirSync(executionRoot,{mode:0o700});}catch(error){if(error.code!=='EEXIST')throw error;}syncDirectory(directory);}
  let stat;try{stat=fs.lstatSync(executionRoot);}catch(error){if(error.code==='ENOENT'&&!create)return false;throw error;}
  if(!stat.isDirectory()||stat.isSymbolicLink())throw new Error('Unsafe execution journal');
  return true;
 }
 function pendingExecutions(){
  assertNotMaintained();
  if(!executionDirectory())return 0;
  const dir=fs.opendirSync(executionRoot);let count=0;
  try{while(dir.readSync()!==null){if(++count>limit)throw new Error('Execution journal exceeds limit');}}finally{dir.closeSync();}
  // Even unknown, partial or corrupt entries block admission. Never recover
  // them as receipts or follow links/read their contents.
  return count;
 }
 function beginExecution(task){
  assertNotMaintained();
  if(task?.generation!==generation||!validID(task.session_id)||!validID(task.task_id))throw new Error('Invalid execution identity');
  executionDirectory(true);
  if(pendingExecutions()>=limit)throw new Error('Execution journal full');
  const token=randomUUID(),file=path.join(executionRoot,token+'.json');
  const record={version:1,namespace,generation,session_id:task.session_id,task_id:task.task_id};
  const fd=fs.openSync(file,'wx',0o600);
  try{fs.writeFileSync(fd,JSON.stringify(record));fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
  syncDirectory(executionRoot);
  assertNotMaintained();
  ownedExecutions.set(token,{identity:identity(task),stat:fs.lstatSync(file,{bigint:true})});
  return token;
 }
 function completeExecution(token,receipt){
  assertNotMaintained();
  const owned=ownedExecutions.get(token);
  if(!owned||owned.identity!==identity(receipt))throw new Error('Execution is not owned by this process');
  validate(receipt);
  // Only an authenticated, durably published settled receipt permits removal.
  if(JSON.stringify(read(path.join(root,hash(identity(receipt))+'.json')))!==JSON.stringify(receipt))throw new Error('Conflicting execution receipt');
  syncDirectory(root);executionDirectory();
  const file=path.join(executionRoot,token+'.json'),stat=fs.lstatSync(file,{bigint:true});
  if(!stat.isFile()||stat.isSymbolicLink()||stat.dev!==owned.stat.dev||stat.ino!==owned.stat.ino||stat.size!==owned.stat.size||stat.mtimeNs!==owned.stat.mtimeNs)throw new Error('Execution journal changed');
  fs.unlinkSync(file);syncDirectory(executionRoot);ownedExecutions.delete(token);
 }
 const identity=r=>JSON.stringify([r.generation,r.session_id,r.task_id]);
 const validate=r=>{
  if(!r||Object.keys(r).sort().join(',')!=='cancelled,generation,session_id,task_id'||r.generation!==generation||!validID(r.session_id)||!validID(r.task_id)||typeof r.cancelled!=='boolean')throw new Error('Invalid receipt');
 };
 const sign=r=>createHmac('sha256',apiKey).update(JSON.stringify([namespace,r.generation,r.session_id,r.task_id,r.cancelled])).digest('hex');
 function read(file,verifyName=true) {
  const stat=fs.lstatSync(file);
  if(!stat.isFile()||stat.isSymbolicLink()||stat.size>2048)throw new Error('Unsafe receipt file');
  const fd=fs.openSync(file,fs.constants.O_RDONLY|(fs.constants.O_NOFOLLOW??0));
  let value;try {const opened=fs.fstatSync(fd);if(!opened.isFile()||opened.size>2048)throw new Error('Unsafe receipt file');const buffer=Buffer.alloc(2049);const size=fs.readSync(fd,buffer,0,buffer.length,0);if(size>2048)throw new Error('Oversized receipt');value=JSON.parse(buffer.subarray(0,size).toString('utf8'));}finally{fs.closeSync(fd);}
  validate(value.receipt);
  if(typeof value.mac!=='string'||!/^[a-f0-9]{64}$/.test(value.mac)||!timingSafeEqual(Buffer.from(sign(value.receipt),'hex'),Buffer.from(value.mac,'hex')))throw new Error('Receipt authentication failed');
  if(verifyName&&path.basename(file)!==hash(identity(value.receipt))+'.json')throw new Error('Receipt identity mismatch');
  return Object.freeze(value.receipt);
 }
 function files(){const names=fs.readdirSync(root).filter(n=>n.endsWith('.json'));if(names.length>limit)throw new Error('Receipt queue exceeds limit');return names;}
 // A temporary file may be the ONLY copy of a settled receipt. Never delete
 // it merely because it is old or malformed. Only an authenticated published
 // hard-link duplicate can be removed, after a conservative age threshold.
 function maintainTemporaryFiles({now=Date.now(),minAgeMs=86400000,maxEntries=128}={}) {
  assertNotMaintained();
  if(!Number.isSafeInteger(now)||now<0||!Number.isSafeInteger(minAgeMs)||minAgeMs<86400000||!Number.isSafeInteger(maxEntries)||maxEntries<1||maxEntries>1024)throw new Error('Invalid receipt maintenance options');
  const result={scanned:0,removed:0,retained:0,errors:0,truncated:false};
  const published=new Map();
  // A corrupt published receipt still fails list() closed; it is never used
  // as evidence that deleting another file is safe.
  for(const name of files()) {
   const target=path.join(root,name);
   try{read(target);const stat=fs.lstatSync(target,{bigint:true});if(stat.isFile()&&!stat.isSymbolicLink())published.set(`${stat.dev}:${stat.ino}`,target);}catch{result.errors++;}
  }
  const dir=fs.opendirSync(root);
  try{
   for(let entry;(entry=dir.readSync())!==null;) {
    if(!entry.name.startsWith('.pending-'))continue;
    if(result.scanned===maxEntries){result.truncated=true;break;}
    result.scanned++;
    const temp=path.join(root,entry.name);
    try{
     const stat=fs.lstatSync(temp,{bigint:true}),target=published.get(`${stat.dev}:${stat.ino}`);
     if(!/^\.pending-[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/.test(entry.name)||!stat.isFile()||stat.isSymbolicLink()||stat.nlink<2n||!target||BigInt(now)-stat.mtimeMs<BigInt(minAgeMs)){result.retained++;continue;}
     read(target);
     const current=fs.lstatSync(temp,{bigint:true}),copy=fs.lstatSync(target,{bigint:true});
     if(!current.isFile()||current.isSymbolicLink()||!copy.isFile()||copy.isSymbolicLink()||current.dev!==stat.dev||current.ino!==stat.ino||copy.dev!==stat.dev||copy.ino!==stat.ino||current.mtimeNs!==stat.mtimeNs){result.retained++;continue;}
     fs.unlinkSync(temp);result.removed++;
    }catch(error){if(error.code!=='ENOENT'){result.errors++;result.retained++;}}
   }
  }finally{dir.closeSync();}
  return Object.freeze(result);
 }
 function list(){return files().map(name=>read(path.join(root,name)));}
 // put() is called only for settled executions. A complete signed temporary
 // envelope therefore preserves the original receipt; partial/foreign files
 // cannot manufacture evidence that a task has stopped.
 let recoveryDir,cycleBlocked=false;
 function closeRecovery(){if(recoveryDir){recoveryDir.closeSync();recoveryDir=undefined;}}
 function recoverUnpublished({maxEntries=128}={}) {
  assertNotMaintained();
  if(!Number.isSafeInteger(maxEntries)||maxEntries<1||maxEntries>1024)throw new Error('Invalid receipt recovery options');
  const result={scanned:0,recovered:0,retained:0,errors:0,truncated:false,capacityBlocked:false};
  if(!recoveryDir){recoveryDir=fs.opendirSync(root);cycleBlocked=false;}
  try{
   // Bound all directory entries, not just valid-looking temporary names.
   // Keep the iterator between passes so corrupt/unknown names cannot starve
   // valid receipts later in the directory. Never cache file contents.
   for(let visited=0;visited<maxEntries;visited++) {
    const entry=recoveryDir.readSync();
    if(entry===null){closeRecovery();result.capacityBlocked=cycleBlocked;return Object.freeze(result);}
    if(!/^\.pending-[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/.test(entry.name))continue;
    result.scanned++;
    const temp=path.join(root,entry.name);
    try{
     const before=fs.lstatSync(temp,{bigint:true}),receipt=read(temp,false);
     // Reuse authenticated immutable publication, queue bounds and conflict
     // detection. Do not replace a published receipt with a different result.
     put(receipt);
     const after=fs.lstatSync(temp,{bigint:true});
     if(before.dev!==after.dev||before.ino!==after.ino||before.mtimeNs!==after.mtimeNs||after.isSymbolicLink()){result.retained++;continue;}
     fs.unlinkSync(temp);result.recovered++;
    }catch(error){if(error.code!=='ENOENT'){result.retained++;result.errors++;if(error.code==='RECEIPT_QUEUE_FULL')cycleBlocked=true;}}
   }
   result.truncated=true;result.capacityBlocked=cycleBlocked;
  }catch(error){closeRecovery();throw error;}
  return Object.freeze(result);
 }
 function put(receipt) {
  assertNotMaintained();
  validate(receipt);const target=path.join(root,hash(identity(receipt))+'.json');
  if(fs.existsSync(target)){if(JSON.stringify(read(target))!==JSON.stringify(receipt))throw new Error('Conflicting receipt');syncDirectory(root);return;}
  if(files().length>=limit)throw Object.assign(new Error('Receipt queue full'),{code:'RECEIPT_QUEUE_FULL'});
  const temp=path.join(root,'.pending-'+randomUUID());let fd;
  try {
   fd=fs.openSync(temp,'wx',0o600);fs.writeFileSync(fd,JSON.stringify({receipt,mac:sign(receipt)}));fs.fsyncSync(fd);fs.closeSync(fd);fd=undefined;
   // Publish an immutable complete file; never replace another writer's receipt.
   try{fs.linkSync(temp,target);}catch(error){if(error.code!=='EEXIST')throw error;if(JSON.stringify(read(target))!==JSON.stringify(receipt))throw new Error('Conflicting receipt');}
   syncDirectory(root);
  } finally {if(fd!==undefined)fs.closeSync(fd);try{fs.unlinkSync(temp);}catch(error){if(error.code!=='ENOENT')throw error;}}
 }
 function remove(receipt){assertNotMaintained();const target=path.join(root,hash(identity(receipt))+'.json');try{if(JSON.stringify(read(target))!==JSON.stringify(receipt))throw new Error('Conflicting receipt');fs.unlinkSync(target);}catch(error){if(error.code!=='ENOENT')throw error;}}
 return Object.freeze({list,put,remove,maintainTemporaryFiles,recoverUnpublished,closeRecovery,pendingExecutions,beginExecution,completeExecution});
}
