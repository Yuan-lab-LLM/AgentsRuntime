import fs from 'node:fs';
import path from 'node:path';
import {randomUUID,createHash} from 'node:crypto';
const fail=code=>{const error=new Error(code);error.code=code;throw error;};
const same=(a,b)=>a.dev===b.dev&&a.ino===b.ino;
function syncDirectory(directory){
 if(process.platform==='win32')return;
 const fd=fs.openSync(directory,fs.constants.O_RDONLY|(fs.constants.O_DIRECTORY??0)|(fs.constants.O_NOFOLLOW??0));
 try{fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
}
// Only cooperating writers on one trusted filesystem. There is no timeout,
// PID-based takeover or assumption that a disconnected process has stopped.
export function acquireRuntimeWriter({directory,generation,isQuiescent=()=>false}={}){
 if(typeof directory!=='string'||!path.isAbsolute(directory)||path.resolve(directory)===path.parse(directory).root||!Number.isSafeInteger(generation)||generation<0||generation>2147483647||typeof isQuiescent!=='function')fail('INVALID_WRITER_CONFIGURATION');
 const root=path.resolve(directory),rootStat=fs.lstatSync(root,{bigint:true});
 const realRoot=path.resolve(fs.realpathSync(root));
 if(!rootStat.isDirectory()||rootStat.isSymbolicLink()||(process.platform==='win32'?realRoot.toLowerCase()!==root.toLowerCase():realRoot!==root))fail('UNSAFE_WRITER_DIRECTORY');
 if(process.platform!=='win32'&&(rootStat.uid!==BigInt(process.geteuid())||(rootStat.mode&0o077n)!==0n))fail('UNSAFE_WRITER_DIRECTORY');
 const lock=path.join(root,'.task-writer.lock'),owner=path.join(lock,'owner.json');
 const assertNotMaintained=()=>{try{fs.lstatSync(path.join(root,'.task-maintenance.lock'));}catch(error){if(error.code==='ENOENT')return;throw error;}fail('WRITER_MAINTENANCE_ACTIVE');};
 const rootCurrent=()=>{const current=fs.lstatSync(root,{bigint:true});if(!same(rootStat,current)||!current.isDirectory()||current.isSymbolicLink())fail('WRITER_DIRECTORY_CHANGED');};
 rootCurrent();
 assertNotMaintained();
 try{fs.mkdirSync(lock,{mode:0o700});}catch(error){if(error.code==='EEXIST')fail('WRITER_ALREADY_CLAIMED');fail('WRITER_STORAGE_UNAVAILABLE');}
 // Once mkdir succeeds, errors deliberately retain the barrier for inspection.
 const lockStat=fs.lstatSync(lock,{bigint:true});
 const contents=JSON.stringify({version:1,generation,owner:randomUUID()});
 try{
  const fd=fs.openSync(owner,'wx',0o600);
  try{fs.writeFileSync(fd,contents);fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
  syncDirectory(lock);syncDirectory(root);rootCurrent();assertNotMaintained();
 }catch{fail('WRITER_CLAIM_UNCONFIRMED');}
 const ownerStat=fs.lstatSync(owner,{bigint:true});let released=false;
 function assertOwned(){
  if(released)fail('WRITER_RELEASED');rootCurrent();assertNotMaintained();
  const stat=fs.lstatSync(lock,{bigint:true}),fileStat=fs.lstatSync(owner,{bigint:true});
  if(!stat.isDirectory()||stat.isSymbolicLink()||!same(stat,lockStat)||!fileStat.isFile()||fileStat.isSymbolicLink()||!same(fileStat,ownerStat)||fileStat.size!==BigInt(Buffer.byteLength(contents)))fail('WRITER_OWNERSHIP_CHANGED');
  const fd=fs.openSync(owner,fs.constants.O_RDONLY|(fs.constants.O_NOFOLLOW??0));
  try{
   const opened=fs.fstatSync(fd,{bigint:true});
   // Windows handle and path stats need not expose identical inode metadata.
   // Linux production retains the stronger open-handle identity check.
   if(!opened.isFile()||opened.size!==ownerStat.size||(process.platform!=='win32'&&!same(opened,ownerStat))||fs.readFileSync(fd,'utf8')!==contents)fail('WRITER_OWNERSHIP_CHANGED');
   if(!same(fs.lstatSync(owner,{bigint:true}),ownerStat))fail('WRITER_OWNERSHIP_CHANGED');
  }finally{fs.closeSync(fd);}
  return true;
 }
 function release(){
  if(released)return;
  // This is a caller-provided proof boundary, not an automatic exit hook.
  if(isQuiescent()!==true)fail('WRITER_NOT_QUIESCENT');
  assertOwned();
  if(isQuiescent()!==true)fail('WRITER_NOT_QUIESCENT');
  if(fs.readdirSync(lock).sort().join(',')!=='owner.json')fail('WRITER_OWNERSHIP_CHANGED');
  fs.unlinkSync(owner);syncDirectory(lock);fs.rmdirSync(lock);syncDirectory(root);released=true;
 }
 return Object.freeze({assertOwned,release,claimID:createHash('sha256').update(contents).digest('hex')});
}
