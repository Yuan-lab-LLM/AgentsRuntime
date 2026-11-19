import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {acquireRuntimeWriter} from './runtime_writer_guard.mjs';
function fixture(t){const directory=fs.mkdtempSync(path.join(os.tmpdir(),'cm-writer-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));return directory;}
test('maintenance prevents acquisition and invalidates a live writer check',t=>{
 const directory=fixture(t),maintenance=path.join(directory,'.task-maintenance.lock');
 fs.mkdirSync(maintenance);
 assert.throws(()=>acquireRuntimeWriter({directory,generation:1}),{code:'WRITER_MAINTENANCE_ACTIVE'});
 assert.equal(fs.existsSync(path.join(directory,'.task-writer.lock')),false);
 fs.rmdirSync(maintenance);
 const writer=acquireRuntimeWriter({directory,generation:1,isQuiescent:()=>true});
 fs.mkdirSync(maintenance);
 assert.throws(()=>writer.assertOwned(),{code:'WRITER_MAINTENANCE_ACTIVE'});
 assert.throws(()=>writer.release(),{code:'WRITER_MAINTENANCE_ACTIVE'});
});
test('exclusive claim is generation independent and release requires quiescence',t=>{
 const directory=fixture(t);let idle=false;
 const owner=acquireRuntimeWriter({directory,generation:3,isQuiescent:()=>idle});
 assert.equal(owner.assertOwned(),true);
 for(const generation of [3,4])assert.throws(()=>acquireRuntimeWriter({directory,generation}),{code:'WRITER_ALREADY_CLAIMED'});
 assert.throws(()=>owner.release(),{code:'WRITER_NOT_QUIESCENT'});idle=true;owner.release();owner.release();
 assert.throws(()=>owner.assertOwned(),{code:'WRITER_RELEASED'});
 const next=acquireRuntimeWriter({directory,generation:4,isQuiescent:()=>true});next.release();
});
test('default release cannot infer stopped tasks',t=>{
 const directory=fixture(t);const owner=acquireRuntimeWriter({directory,generation:1});
 assert.throws(()=>owner.release(),{code:'WRITER_NOT_QUIESCENT'});assert.ok(fs.existsSync(path.join(directory,'.task-writer.lock')));
});
test('owner tampering and unknown entries retain barrier',t=>{
 const directory=fixture(t);const owner=acquireRuntimeWriter({directory,generation:1,isQuiescent:()=>true});
 fs.writeFileSync(path.join(directory,'.task-writer.lock','owner.json'),'{}');
 assert.throws(()=>owner.assertOwned());assert.throws(()=>owner.release());assert.ok(fs.existsSync(path.join(directory,'.task-writer.lock')));
});
test('foreign partial and old claims are never stolen',t=>{
 const directory=fixture(t);fs.mkdirSync(path.join(directory,'.task-writer.lock'));
 fs.utimesSync(path.join(directory,'.task-writer.lock'),new Date(0),new Date(0));
 assert.throws(()=>acquireRuntimeWriter({directory,generation:2}),{code:'WRITER_ALREADY_CLAIMED'});
});
test('quiescence is rechecked before removal',t=>{
 const directory=fixture(t);let calls=0;const owner=acquireRuntimeWriter({directory,generation:1,isQuiescent:()=>++calls===1});
 assert.throws(()=>owner.release(),{code:'WRITER_NOT_QUIESCENT'});assert.equal(owner.assertOwned(),true);
});
test('actual competing processes produce one winner and no automatic exit release',async t=>{
 const directory=fixture(t),script=new URL('./runtime_writer_guard.mjs',import.meta.url).href;
 const code=`import {acquireRuntimeWriter} from ${JSON.stringify(script)};try{acquireRuntimeWriter({directory:process.argv[1],generation:1});process.stdout.write('claimed');}catch(e){process.stdout.write(e.code??'failed');}`;
 const run=()=>new Promise((resolve,reject)=>{const child=spawn(process.execPath,['--input-type=module','-e',code,directory]);let output='';child.stdout.on('data',b=>output+=b);child.on('error',reject);child.on('close',exit=>exit===0?resolve(output):reject(new Error('child failed')));});
 const results=await Promise.all([run(),run()]);assert.deepEqual(results.sort(),['WRITER_ALREADY_CLAIMED','claimed']);
 assert.throws(()=>acquireRuntimeWriter({directory,generation:2}),{code:'WRITER_ALREADY_CLAIMED'});
});
test('unsafe configuration never creates a claim',t=>{
 const directory=fixture(t);
 for(const generation of [-1,2147483648,1.2])assert.throws(()=>acquireRuntimeWriter({directory,generation}),{code:'INVALID_WRITER_CONFIGURATION'});
 assert.equal(fs.existsSync(path.join(directory,'.task-writer.lock')),false);
});
