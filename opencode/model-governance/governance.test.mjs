import test from "node:test"
import assert from "node:assert/strict"
import { createGovernance, ModelUnavailable, GovernanceUnavailable } from "./governance.mjs"
const env={CLAWMANAGER_LLM_BASE_URL:"https://gateway/api/v1/gateway",CLAWMANAGER_LLM_API_KEY:"test-only",CLAWMANAGER_GATEWAY_GENERATION:"7"}
const json=(data,status=200)=>new Response(JSON.stringify(data),{status,headers:{"Content-Type":"application/json"}})
test("old managed aliases resolve only to the same authorized deployment without duplicating the picker", async () => {
 let now = 10000, fail = false
 let entries = [{ id: "renamed", stable_id: "cm-deploy-v1-test", aliases: ["old model"], tools: true, context_window: 4096 }]
 const g = createGovernance({ env, now: () => now, fetcher: async () => fail ? json({}, 503) : json({ models: entries }) })
 const template = { id: "auto", providerID: "clawmanager", api: { id: "auto" }, capabilities: {}, limit: { context: 8192 } }
 const other = { id: "old model", providerID: "custom" }
 const source = [template, other]
 assert.deepEqual((await g.projectCatalog(source)).map(x => x.id), ["old model", "cm-deploy-v1-test"])
 for (const ref of ["old model", "OLD MODEL", "cm-deploy-v1-test", "renamed"]) {
  const model = await g.resolveCatalogModel(source, "clawmanager", ref)
  assert.equal(model.id, ref); assert.equal(model.api.id, "cm-deploy-v1-test"); assert.equal(model.limit.context, 4096)
 }
 assert.equal(await g.resolveCatalogModel(source, "custom", "old model"), other)
 assert.equal(await g.resolveCatalogModel(source, "custom", "cm-deploy-v1-test"), undefined)
 await assert.rejects(g.resolveCatalogModel(source, "clawmanager", "unknown"), ModelUnavailable)
 now += 4000; fail = true
 await assert.rejects(g.resolveCatalogModel(source, "clawmanager", "old model"), GovernanceUnavailable)
 fail = false; entries = []
 await assert.rejects(g.resolveCatalogModel(source, "clawmanager", "old model"), ModelUnavailable)
 // Neither a successful empty catalog nor a deletion permits auto fallback.
 assert.deepEqual(await g.projectCatalog(source), [other])
 assert.equal(template.id, "auto")
})
test("ambiguous or malformed historical aliases fail closed", async () => {
 const template = { id: "auto", providerID: "clawmanager", api: {}, capabilities: {}, limit: {} }
 for (const entries of [
  [{ id: "one", aliases: ["old"] }, { id: "two", aliases: ["old"] }],
  [{ id: "one", aliases: "old" }], [{ id: "one", aliases: [null] }], [{ id: "one", stable_id: 1 }],
 ]) {
  const g = createGovernance({ env, fetcher: async () => json({ models: entries }) })
  await assert.rejects(g.resolveCatalogModel([template], "clawmanager", "old"))
 }
})
test("turn identity remains stable across tools and changes for a new user message",async()=>{
 const calls=[];const g=createGovernance({env,fetcher:async(url,init)=>{calls.push([url,init]);return json({})}})
 const a=await g.begin("ses_a",[{type:"user",id:"msg_one"}])
 const b=await g.begin("ses_a",[{type:"user",id:"msg_one"},{type:"assistant",id:"msg_tool"}])
 assert.equal(a,b);assert.equal(g.headers(a)["X-ClawManager-Runtime-Generation"],"7")
 assert.notEqual(await g.begin("ses_a",[{type:"user",id:"msg_two"}]),a)
 assert.equal(calls[0][1].redirect,"error")
 assert.ok(!calls[0][1].body.includes("test-only"))
})
test("revocation is sticky and does not affect other tasks; only finish sends ack",async()=>{
 let denied=false;const receipts=[]
 const g=createGovernance({env,fetcher:async(url,init)=>{
  if(url.endsWith("finish")){receipts.push(JSON.parse(init.body));return json({accepted:true})}
  const body=JSON.parse(init.body);return json({},denied&&body.task_id==="one"?403:200)
 }})
 const a=await g.begin("s",[{type:"user",id:"one"}]);const b=await g.begin("t",[{type:"user",id:"two"}])
 denied=true;await assert.rejects(g.check(a),ModelUnavailable);await g.check(b);assert.equal(receipts.length,0)
 denied=false;await assert.rejects(g.check(a),ModelUnavailable)
 await g.finishSession("s");assert.equal(receipts[0].cancelled,true);assert.equal(receipts.length,1)
})
test("no anonymous main fallback; transport failure fails closed",async()=>{
 const g=createGovernance({env,fetcher:async()=>{throw new Error("offline")}})
 await assert.rejects(g.begin("s",[]),GovernanceUnavailable)
 await assert.rejects(g.begin("s",[{type:"user",id:"u"}]),GovernanceUnavailable)
})
test("catalog removes revoked entries, adds newly allowed entries, and never serves stale grants on failure",async()=>{
 let now=10000,fail=false,entries=[{id:"auto",context_window:8192,tools:true},{id:"new",context_window:4096,tools:false}]
 const g=createGovernance({env,now:()=>now,fetcher:async()=>fail?json({},500):json({models:entries})})
 const original=[{id:"old",name:"Old",providerID:"clawmanager",api:{id:"old"},limit:{context:9000,output:1024},capabilities:{tools:true}},{id:"local",providerID:"other"}]
 let result=await g.projectCatalog(original)
 assert.deepEqual(result.map(x=>x.id),["local","auto","new"]);assert.equal(result[2].api.id,"new");assert.equal(result[2].limit.context,4096)
 now+=4000;fail=true;await assert.rejects(g.projectCatalog(original),GovernanceUnavailable)
 // Recovery must not require deleting the session or restarting the client.
 fail=false;entries=[{id:"auto",context_window:8192,tools:true}]
 result=await g.projectCatalog(original);assert.deepEqual(result.map(x=>x.id),["local","auto"])
 // A successful empty response really does revoke all managed entries.
 now+=4000;entries=[]
 result=await g.projectCatalog(original);assert.deepEqual(result.map(x=>x.id),["local"])
})
test("context changes refresh an existing projected catalog without replacing output budget",async()=>{
 let now=10000, window=64000
 const g=createGovernance({env,now:()=>now,fetcher:async()=>json({models:[{id:"auto",context_window:window,tools:true}]})})
 const first=await g.projectCatalog([{id:"auto",providerID:"clawmanager",api:{id:"auto"},limit:{context:128000,output:1024}}])
 assert.equal(first[0].limit.context,64000)
 window=32000;now+=4000
 const next=await g.projectCatalog(first)
 assert.equal(next[0].limit.context,32000)
 assert.equal(next[0].limit.output,1024)
})
test("unmanaged runtimes are unchanged",async()=>{
 const g=createGovernance({env:{},fetcher:()=>{throw new Error("must not call")}})
 assert.equal(await g.begin("s",[]),undefined);assert.deepEqual(await g.projectCatalog([{id:"m"}]),[{id:"m"}])
})

test("a failed receipt is retried without falsely claiming success",async()=>{
 let fails=true,attempts=0
 const g=createGovernance({env,fetcher:async(url)=>{
  if(url.endsWith("finish")){attempts++;if(fails)return json({},503)}
  return json({})
 }})
 await g.begin("s",[{type:"user",id:"one"}]);await g.finishSession("s");assert.equal(attempts,1)
 fails=false;await g.flushReceipts();assert.equal(attempts,2)
 await g.flushReceipts();assert.equal(attempts,2)
})

test("compaction keeps the authenticated active turn but cannot invent or revive it",async()=>{
 const g=createGovernance({env,fetcher:async()=>json({})})
 const task=await g.begin("s",[{type:"user",id:"one"}])
 assert.equal(await g.begin("s",[{type:"compaction",id:"summary"}]),task)
 await assert.rejects(g.begin("other",[{type:"compaction",id:"summary"}]),GovernanceUnavailable)
 await g.finishSession("s")
 await assert.rejects(g.begin("s",[{type:"compaction",id:"summary"}]),GovernanceUnavailable)
})

test("an unspecified context limit preserves the runtime default",async()=>{
 const g=createGovernance({env,fetcher:async()=>json({models:[{id:"auto",context_window:0,tools:true}]})})
 const result=await g.projectCatalog([{id:"auto",providerID:"clawmanager",api:{id:"auto"},limit:{context:8192,output:1024},capabilities:{tools:true}}])
 assert.equal(result[0].limit.context,8192)
})
