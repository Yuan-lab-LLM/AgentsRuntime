import test from "node:test";
import assert from "node:assert/strict";
import {NativeClient,nativeIdentity} from "./native-client.mjs";
const identity={teamId:"170",memberId:"worker",taskId:"team-170-task-373",messageId:"original",generation:5};
for(const outcome of ["succeeded","failed","interrupted"]){
 test(`materialized native records work with default nonpersistent bus: ${outcome}`,async()=>{
  const c=new NativeClient({port:4096,password:"private-test"}),ids=nativeIdentity(identity);let pages=0;
  c.api=async url=>{
   if(url.endsWith(ids.message))return {data:{id:ids.message,type:"user"}};
   const cursor=JSON.parse(Buffer.from(new URL("http://local"+url).searchParams.get("cursor"),"base64url").toString());
   assert.equal(cursor.id,ids.message);assert.equal(cursor.order,"asc");assert.equal(cursor.direction,"next");pages++;
   return {data:[{id:"msg_tool",type:"assistant",finish:"tool-calls",time:{created:1,completed:2},content:[{type:"text",text:"Intermediate"},{type:"reasoning",text:"private-canary"},{type:"tool",name:"team_complete_task",state:{status:"completed",input:{secret:"canary"}}}]},
    {id:"msg_public",type:"assistant",finish:"stop",time:{created:3,completed:4},content:[{type:"text",text:"Full actual public answer"},{type:"reasoning",text:"private-canary"}]},
    {id:"msg_idle",type:"idle",outcome,time:{created:5}},
    {id:"msg_other",type:"user"},{id:"msg_foreign",type:"assistant",finish:"stop",time:{completed:6},content:[{type:"text",text:"Foreign answer"}]}]};
  };
  const facts=await c.inspectTurn(identity);
  assert.equal(facts.consumed,true);assert.equal(facts.failed,outcome!=="succeeded");assert.equal(pages,1);
  assert.equal(facts.publicText.text,"Full actual public answer");
  assert.ok(!JSON.stringify(facts).includes("canary"));assert.ok(!JSON.stringify(facts).includes("Intermediate"));assert.ok(!JSON.stringify(facts).includes("Foreign answer"));
 });
}
test("another user boundary and malformed continuation cannot authorize native delivery",async()=>{
 const c=new NativeClient({port:4096,password:"test"}),ids=nativeIdentity(identity);
 c.api=async url=>url.endsWith(ids.message)?{data:{id:ids.message,type:"user"}}:{data:[{type:"user",id:"other"},{type:"assistant",id:"foreign",finish:"stop",time:{completed:1},content:[{type:"text",text:"Foreign"}]}]};
 assert.equal((await c.inspectTurn(identity)).publicText,undefined);
});
