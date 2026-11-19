import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {createShutdownReceiptStore} from './shutdown_receipt_store.mjs';
import {acquireRuntimeWriter} from './runtime_writer_guard.mjs';
const baseURL='http://gateway.invalid/api/gateway/llm',apiKey='shutdown-test-private-key',generation=7;
const claimID='a'.repeat(64);
function fixture(t){
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-shutdown-receipt-'));
 t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
 return {directory,store:createShutdownReceiptStore({directory,baseURL,apiKey,generation})};
}
const files=directory=>fs.readdirSync(path.join(directory,'z-runtime-shutdowns'));
test('signed cleanup survives recreation without claiming release',t=>{
 const {directory,store}=fixture(t),result=store.write(claimID);
 assert.equal(result.native_cleanup_confirmed,true);assert.equal(result.claim_id,claimID);assert.equal('released' in result,false);
 assert.equal(files(directory).length,1);
 const bytes=fs.readFileSync(path.join(directory,'z-runtime-shutdowns',files(directory)[0]),'utf8');
 assert.ok(!bytes.includes(apiKey));assert.ok(!bytes.includes(baseURL));
 assert.deepEqual(createShutdownReceiptStore({directory,baseURL,apiKey,generation}).read(claimID),result);
 store.write(claimID);assert.equal(files(directory).length,1);
});
test('different credentials, endpoint, generation or claim cannot adopt old evidence',t=>{
 const {directory,store}=fixture(t);store.write(claimID);
 for(const changes of [{apiKey:'other'},{baseURL:'http://other.invalid'},{generation:8}])assert.throws(()=>createShutdownReceiptStore({directory,baseURL,apiKey,generation,...changes}).read(claimID));
 assert.throws(()=>store.read('b'.repeat(64)));assert.equal(files(directory).length,1);
});
test('tampered native confirmation and mac never authorize read or overwrite',t=>{
 const {directory,store}=fixture(t);store.write(claimID);
 const file=path.join(directory,'z-runtime-shutdowns',files(directory)[0]),value=JSON.parse(fs.readFileSync(file,'utf8'));
 value.record.native_cleanup_confirmed=false;fs.writeFileSync(file,JSON.stringify(value));const before=fs.readFileSync(file,'utf8');
 assert.throws(()=>store.read(claimID));assert.throws(()=>store.write(claimID));assert.equal(fs.readFileSync(file,'utf8'),before);
});
test('missing evidence read is non-mutating and unsafe identifiers are rejected',t=>{
 const {directory,store}=fixture(t);assert.throws(()=>store.read(claimID));
 assert.equal(fs.existsSync(path.join(directory,'z-runtime-shutdowns')),false);
 for(const id of ['../outside','','A'.repeat(64)])assert.throws(()=>store.write(id),{code:'INVALID_SHUTDOWN_CLAIM'});
});
test('claim binding distinguishes runs even at the same generation',t=>{
 const {directory,store}=fixture(t);
 const first=acquireRuntimeWriter({directory,generation,isQuiescent:()=>true});
 const ownerBytes=fs.readFileSync(path.join(directory,'.task-writer.lock','owner.json'));
 assert.equal(first.claimID,createHash('sha256').update(ownerBytes).digest('hex'));
 store.write(first.claimID);first.release();
 const next=acquireRuntimeWriter({directory,generation,isQuiescent:()=>true});
 assert.notEqual(first.claimID,next.claimID);assert.throws(()=>store.read(next.claimID));next.release();
});
test('publication failure retains live claim and does not manufacture a final receipt',t=>{
 const {directory,store}=fixture(t),writer=acquireRuntimeWriter({directory,generation,isQuiescent:()=>true});
 const link=fs.linkSync;fs.linkSync=()=>{throw Object.assign(new Error('injected'),{code:'EIO'});};
 try{assert.throws(()=>{store.write(writer.claimID);writer.release();});}finally{fs.linkSync=link;}
 assert.equal(writer.assertOwned(),true);assert.throws(()=>store.read(writer.claimID));
 assert.ok(files(directory).every(name=>name.startsWith('.pending-')));
});
test('unbounded historical records block new writes without deleting evidence',t=>{
 const {directory,store}=fixture(t),root=path.join(directory,'z-runtime-shutdowns');fs.mkdirSync(root);
 for(let i=0;i<256;i++)fs.writeFileSync(path.join(root,'unknown-'+i),'evidence');
 assert.throws(()=>store.write(claimID),{code:'SHUTDOWN_RECEIPT_LIMIT'});assert.equal(files(directory).length,256);
});
