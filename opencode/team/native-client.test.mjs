import test from "node:test";
import assert from "node:assert/strict";
import { NativeClient, nativeIdentity } from "./native-client.mjs";

const identity = { teamId: "12", memberId: "writer", taskId: "team-12-task-19", messageId: "delivery-1", generation: 2 };
const session = nativeIdentity(identity).session;
const owned = { data: { id: session, location: { directory: "/workspaces/member/starter" }, metadata: { "clawmanager.team": "12", "clawmanager.member": "writer" } } };
const response = (data, status = 200) => new Response(JSON.stringify(data), { status });

test("steering keeps the original input identity and signs only this Team context",async()=>{
 const sent=[];
 const client=new NativeClient({port:4096,password:"fixture",request:async(url,options)=>{if(url.endsWith('/active'))return response({data:{[session]:true}});sent.push(JSON.parse(options.body));return response({})}});
 client.contextKey='a'.repeat(64);
 await client.steer(identity,'Current task reminder','notice');
 const body=sent[0],context={type:'user',id:body.id,metadata:body.metadata};
 assert.equal(body.delivery,'steer');assert(client.isOwnedContext(context,nativeIdentity(identity)));
 assert(!client.isOwnedContext(context,nativeIdentity({...identity,memberId:'other'})));
 assert(!client.isOwnedContext({...context,metadata:{...body.metadata,'clawmanager.contextFor':'unrelated'}},nativeIdentity(identity)));
 assert.notEqual(body.id,nativeIdentity(identity).message);
});

test("pending interaction reports only this Team session and never exports raw requests", async () => {
 const client = new NativeClient({port:4096,password:"test",request:async url => response({data: url.endsWith('/api/form') ? [{id:"own-form",sessionID:session,metadata:{kind:"websearch.provider"},secret:"canary"},{id:"other-form",sessionID:"other"}] : [{id:"own-permission",sessionID:session,resources:["private-canary"]}]})});
 const value=await client.pendingInteraction(identity);
 assert.deepEqual(value,{pending:true,kinds:["permission","search_provider"],ids:["own-form","own-permission"]});
 assert.ok(!JSON.stringify(value).includes("canary"));
});

test("transport identities survive generation recovery and distinguish corrective messages", () => {
 assert.deepEqual(nativeIdentity(identity), nativeIdentity({ ...identity, generation: 3 }));
 assert.notEqual(nativeIdentity(identity).message, nativeIdentity({ ...identity, messageId: "delivery-2" }).message);
 assert.notEqual(nativeIdentity(identity).session, nativeIdentity({ ...identity, memberId: "reviewer" }).session);
 assert.deepEqual(nativeIdentity(identity), nativeIdentity({ ...identity, generation: 0 }));
 assert.throws(() => nativeIdentity({ ...identity, generation: -1 }));
});

test("lost admission reply reuses exact native session and message identity", async () => {
 let exists = false, lost = true; const prompts = [];
 const client = new NativeClient({ port: 4096, password: "test", request: async (url, options) => {
  assert.equal(options.redirect, "error");
  assert.ok(options.headers.Authorization.startsWith("Basic "));
  if (url.endsWith(`/api/session/${session}`)) return response(exists ? owned : {}, exists ? 200 : 404);
  if (url.endsWith("/api/session")) { exists = true; return response(owned); }
  if (url.endsWith("/prompt")) { prompts.push(JSON.parse(options.body)); if (lost) { lost = false; throw new Error("connection lost"); } return response({ data: {} }); }
  throw new Error("unexpected request");
 } });
 await assert.rejects(client.admit(identity, "/workspaces/member/starter", "write report"));
 await client.admit(identity, "/workspaces/member/starter", "write report");
 assert.deepEqual(prompts[0], prompts[1]);
});

test("a conflicting session never receives input", async () => {
 let requests = 0;
 const client = new NativeClient({ port: 4096, password: "test", request: async () => {
  requests++; return response({ data: { ...owned.data, location: { directory: "/other" } } });
 } });
 await assert.rejects(client.admit(identity, "/workspaces/member/starter", "input"), /ownership mismatch/);
 assert.equal(requests, 1);
});

test("interrupt checks actual idle instead of acknowledging HTTP success", async () => {
 let active = true;
 const client = new NativeClient({ port: 4096, password: "test", request: async url =>
  response(url.includes("/interrupt") ? { interrupted: true } : { data: active ? { [session]: {} } : {} })
 });
 await assert.rejects(client.interrupt(identity), /unconfirmed/);
 active = false;
 await client.interrupt(identity);
});

test("native errors do not echo credentials or upstream response bodies", async () => {
 const client = new NativeClient({ port: 4096, password: "private", request: async () => response({ secret: "private" }, 503) });
 await assert.rejects(client.ensureSession(identity, "/workspaces/member/starter"), error => !error.message.includes("private") && error.status === 503);
});

test("durable event log reconnects from an exclusive native cursor and rejects foreign sessions", async () => {
 const event = { type: "session.text.ended", data: { sessionID: session, text: "public answer" }, durable: { aggregateID: session, seq: 9 } };
 let foreign = false;
 const client = new NativeClient({ port: 4096, password: "test", request: async url => {
  assert.ok(url.includes("after=8&follow=false"));
  return new Response(`data: ${JSON.stringify(foreign ? { ...event, data: { sessionID: "ses_other" } } : event)}\n\ndata: ${JSON.stringify({ type: "log.synced", aggregateID: session, seq: 9 })}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
 } });
 const seen = [];
 assert.equal(await client.readLog(identity, 8, event => seen.push(event)), 9);
 assert.equal(seen.length, 1);
 foreign = true;
 await assert.rejects(client.readLog(identity, 8, () => assert.fail()), /ownership mismatch/);
});

test("truncated or failed native logs cannot be interpreted as an idle turn", async () => {
 for (const raw of ['data: {"type":"session.text.ended"}', 'event: failure\ndata: {}\n\n']) {
  const client = new NativeClient({ port: 4096, password: "test", request: async () => new Response(raw, { headers: { "Content-Type": "text/event-stream" } }) });
  await assert.rejects(client.readLog(identity, 0, () => assert.fail()));
 }
});
