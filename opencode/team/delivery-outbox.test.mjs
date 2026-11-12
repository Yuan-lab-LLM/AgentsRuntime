import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {deliveryOutbox} from "./delivery-outbox.mjs";

test("publication survives lost response and process restart without replaying a model",async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),"native-public-delivery-"));
 const envelope={taskId:"team-1-task-1",messageId:"input"},payload={eventId:"native-delivery-"+"a".repeat(64),text:"Actual public answer"};
 let fail=true;const ids=[];
 const publish=async(e,p)=>{assert.deepEqual(e,envelope);ids.push(p.eventId);if(fail)throw Error("lost response");assert.deepEqual(p,payload);};
 try {
  let journal=deliveryOutbox(dir,publish);await journal.save(envelope,payload);
  await assert.rejects(journal.flush());assert.equal((await fs.readdir(dir)).length,1);
  fail=false;journal=deliveryOutbox(dir,publish);await journal.flush();await journal.flush();
  assert.equal((await fs.readdir(dir)).length,0);assert.deepEqual(ids,[payload.eventId,payload.eventId]);
 }finally{await fs.rm(dir,{recursive:true,force:true});}
});
