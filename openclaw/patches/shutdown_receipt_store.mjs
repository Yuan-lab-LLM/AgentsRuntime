import fs from 'node:fs';
import path from 'node:path';
import {createHash,createHmac,timingSafeEqual,randomUUID} from 'node:crypto';
const hash=value=>createHash('sha256').update(value).digest('hex');
const fail=code=>{const e=new Error(code);e.code=code;throw e;};
const validClaim=value=>typeof value==='string'&&/^[a-f0-9]{64}$/.test(value);
function syncDirectory(dir){if(process.platform==='win32')return;const fd=fs.openSync(dir,fs.constants.O_RDONLY|(fs.constants.O_DIRECTORY??0)|(fs.constants.O_NOFOLLOW??0));try{fs.fsyncSync(fd);}finally{fs.closeSync(fd);}}
function checkDirectory(dir){const s=fs.lstatSync(dir);if(!s.isDirectory()||s.isSymbolicLink())fail('UNSAFE_SHUTDOWN_RECEIPT_DIRECTORY');}
export function createShutdownReceiptStore({directory,baseURL,apiKey,generation}={}){
 let url;try{url=new URL(baseURL);}catch{fail('INVALID_SHUTDOWN_RECEIPT_CONFIGURATION');}
 if(typeof directory!=='string'||!path.isAbsolute(directory)||path.resolve(directory)===path.parse(directory).root||
 !['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash||
 typeof apiKey!=='string'||!apiKey.trim()||/[\r\n\0]/.test(apiKey)||!Number.isSafeInteger(generation)||generation<0||generation>2147483647)fail('INVALID_SHUTDOWN_RECEIPT_CONFIGURATION');
 const base=url.href.replace(/\/+$/,''),namespace=hash(JSON.stringify([base,apiKey,generation]));
 const root=path.join(directory,'z-runtime-shutdowns');
 const payload=claimID=>({version:1,namespace,generation,claim_id:claimID,native_cleanup_confirmed:true});
 const sign=r=>createHmac('sha256',apiKey).update(JSON.stringify(['native-cleanup-v1',r.namespace,r.generation,r.claim_id,r.native_cleanup_confirmed])).digest('hex');
 const target=claimID=>{if(!validClaim(claimID))fail('INVALID_SHUTDOWN_CLAIM');return path.join(root,namespace+'-'+claimID+'.json');};
 function checkRoot(){checkDirectory(directory);const real=path.resolve(fs.realpathSync(directory)),expected=path.resolve(directory);if(process.platform==='win32'?real.toLowerCase()!==expected.toLowerCase():real!==expected)fail('UNSAFE_SHUTDOWN_RECEIPT_DIRECTORY');checkDirectory(root);}
 function read(claimID){
  const file=target(claimID);checkRoot();
  const stat=fs.lstatSync(file);if(!stat.isFile()||stat.isSymbolicLink()||stat.size>2048)fail('INVALID_SHUTDOWN_RECEIPT');
  const fd=fs.openSync(file,fs.constants.O_RDONLY|(fs.constants.O_NOFOLLOW??0));let value;
  try{if(!fs.fstatSync(fd).isFile())fail('INVALID_SHUTDOWN_RECEIPT');const bytes=Buffer.alloc(2049),n=fs.readSync(fd,bytes,0,bytes.length,0);if(n>2048)fail('INVALID_SHUTDOWN_RECEIPT');value=JSON.parse(bytes.subarray(0,n).toString('utf8'));}finally{fs.closeSync(fd);}
  if(!value||Object.keys(value).sort().join(',')!=='mac,record'||!value.record||Object.keys(value.record).sort().join(',')!=='claim_id,generation,namespace,native_cleanup_confirmed,version'||JSON.stringify(value.record)!==JSON.stringify(payload(claimID))||
   typeof value.mac!=='string'||!/^[a-f0-9]{64}$/.test(value.mac)||!timingSafeEqual(Buffer.from(value.mac,'hex'),Buffer.from(sign(value.record),'hex')))fail('INVALID_SHUTDOWN_RECEIPT');
  // This proves signed cleanup only; it never asserts release or process exit.
  return Object.freeze({...value.record});
 }
 function write(claimID){
  const file=target(claimID);checkDirectory(directory);
  try{fs.mkdirSync(root,{mode:0o700});syncDirectory(directory);}catch(e){if(e.code!=='EEXIST')throw e;}
  checkRoot();
  try{fs.lstatSync(file);return read(claimID);}catch(e){if(e.code!=='ENOENT')throw e;}
  const dir=fs.opendirSync(root);let count=0;try{while(dir.readSync()!==null)if(++count>=256)fail('SHUTDOWN_RECEIPT_LIMIT');}finally{dir.closeSync();}
  const record=payload(claimID),bytes=JSON.stringify({record,mac:sign(record)});
  const temp=path.join(root,'.pending-'+randomUUID()),fd=fs.openSync(temp,'wx',0o600);
  try{fs.writeFileSync(fd,bytes);fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
  try{fs.linkSync(temp,file);}catch(e){if(e.code!=='EEXIST')throw e;read(claimID);}
  syncDirectory(root);fs.unlinkSync(temp);syncDirectory(root);
  return read(claimID);
 }
 return Object.freeze({read,write});
}
