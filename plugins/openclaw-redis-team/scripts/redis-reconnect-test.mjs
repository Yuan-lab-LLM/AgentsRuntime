import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import net from 'node:net';
import {setTimeout as sleep} from 'node:timers/promises';
const source=(await fs.readFile(new URL('../dist/index.js',import.meta.url),'utf8')).replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";','const definePluginEntry=entry=>entry;');
const {RedisClient,RespParser}=await import(`data:text/javascript;base64,${Buffer.from(source+'\nexport {RedisClient,RespParser};').toString('base64')}`);
let connections=0,writes=0,auths=0,selects=0;const sockets=new Set();
const server=net.createServer(socket=>{
 connections++;sockets.add(socket);socket.on('close',()=>sockets.delete(socket));socket.on('error',()=>{});
 const parser=new RespParser();let authenticated=false,selected=false;
 socket.on('data',data=>{
  parser.push(data);let command;
  while((command=parser.take())!==null){
   if(command[0]==='AUTH'){assert.deepEqual(command,['AUTH','user','password']);authenticated=true;auths++;socket.write('+OK\r\n');}
   else if(command[0]==='SELECT'){assert(authenticated);assert.equal(command[1],'4');selected=true;selects++;socket.write('+OK\r\n');}
   else {assert(authenticated&&selected);if(command[0]==='DROP'){writes++;socket.destroy();}else {assert.equal(command[0],'PING');socket.write('+PONG\r\n');}}
  }
 });
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const c=new RedisClient(`redis://user:password@127.0.0.1:${server.address().port}/4`);
const bounded=p=>Promise.race([p,sleep(3000).then(()=>{throw Error('Transport promise hung')})]);
try {
 await c.connect();assert.equal(await c.command('PING'),'PONG');
 await bounded(assert.rejects(c.command('DROP'),/connection closed/));
 assert.deepEqual(await bounded(Promise.all([c.command('PING'),c.command('PING')])),['PONG','PONG']);
 assert.equal(connections,2,'concurrent callers must share one reconnect');assert.equal(auths,2);assert.equal(selects,2);assert.equal(writes,1,'lost write response must never be replayed');
 c.close();await assert.rejects(c.command('PING'),/client is closed/);assert.equal(connections,2);
 console.log('PASS: lease-style socket close, bounded rejection, authenticated single reconnect, database restored, no uncertain write replay, explicit close retained');
} finally {c.close();for(const s of sockets)s.destroy();await new Promise(r=>server.close(r));}
