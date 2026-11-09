import { test, expect } from "bun:test"
import { Effect, Exit } from "effect"
import { createGovernance, ModelUnavailable } from "../src/clawmanager-governance.mjs"
import { guardTask } from "../src/clawmanager-governance-effect"

test("revocation joins the provider/tool scope before the receipt and leaves another task alone", async () => {
  let revoked = false
  let toolStopped = false
  let receiptAfterStop = false
  const server = Bun.serve({ port: 0, hostname: "127.0.0.1", async fetch(request) {
    const body = await request.json() as { task_id?: string; cancelled?: boolean }
    if (request.url.endsWith("finish")) {
      receiptAfterStop = toolStopped && body.cancelled === true
      return Response.json({accepted:true})
    }
    return Response.json({}, {status:revoked && body.task_id === "one" ? 403 : 200})
  }})
  try {
    const client=createGovernance({env:{CLAWMANAGER_LLM_BASE_URL:server.url.toString().replace(/\/$/,""),CLAWMANAGER_LLM_API_KEY:"test"}})
    const task=await client.begin("s",[{type:"user",id:"one"}])
    const other=await client.begin("other",[{type:"user",id:"two"}])
    const operation=Effect.sync(()=>{revoked=true}).pipe(Effect.andThen(Effect.never),Effect.ensuring(Effect.sync(()=>{toolStopped=true})))
    const result=await Effect.runPromiseExit(guardTask(operation,task,Effect.void,client).pipe(Effect.ensuring(Effect.promise(()=>client.finishSession("s")))))
    expect(Exit.isFailure(result)).toBe(true)
    expect(toolStopped).toBe(true)
    expect(receiptAfterStop).toBe(true)
    await client.check(other)
    revoked=false
    await expect(client.check(task)).rejects.toBeInstanceOf(ModelUnavailable)
  } finally { server.stop(true) }
})

test("normal completion does not claim a cancellation receipt",async()=>{
 let cancelled: boolean|undefined
 const server=Bun.serve({port:0,hostname:"127.0.0.1",async fetch(request){
   const body=await request.json() as { cancelled?: boolean };if(request.url.endsWith("finish"))cancelled=body.cancelled;return Response.json({})
 }})
 try {
   const client=createGovernance({env:{CLAWMANAGER_LLM_BASE_URL:server.url.toString().replace(/\/$/,""),CLAWMANAGER_LLM_API_KEY:"test"}})
   const task=await client.begin("s",[{type:"user",id:"one"}])
   expect(await Effect.runPromise(guardTask(Effect.succeed("ok"),task,Effect.void,client))).toBe("ok")
   await client.finishSession("s");expect(cancelled).toBe(false)
 } finally {server.stop(true)}
})
