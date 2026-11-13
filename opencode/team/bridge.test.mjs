import test from "node:test";
import assert from "node:assert/strict";
import { createBridge } from "./bridge.mjs";
import { nativeIdentity } from "./native-client.mjs";

test("initial Pro generation zero is preserved and is rejected for a Lite or unidentified host", async () => {
 const cfg = { enabled: true, teamId: "12", memberId: "writer" };
 const protocol = { startAssignmentActivityObserver: async () => ({stop:async()=>{}}), RedisClient: class { async connect(){} close(){} }, taskEvent: (_, event, envelope, extra) => ({ event, ...extra }), eventsKey: () => "events", xaddJson: async () => {}, readChannelConfig: () => cfg, createRuntime: () => ({}) };
 const environment = { CLAWMANAGER_GATEWAY_GENERATION: "0" };
 await assert.rejects(createBridge({ protocol, native: {}, environment }), /identity unavailable/);
 await assert.rejects(createBridge({ protocol, native: {}, environment: { ...environment, CLAWMANAGER_AGENT_ENABLED: "true" } }), /identity unavailable/);
 const bridge = await createBridge({ protocol, native: {}, environment: { ...environment, CLAWMANAGER_AGENT_ENABLED: "true", INSTANCE_ID: "570" } });
 await bridge.stop();
});

test("tools are bound to active native input and never to model supplied Team IDs", async () => {
 let receive, release; const sent = [];
 const cfg = { enabled: true, teamId: "12", memberId: "writer", embeddedTimeoutSeconds: 10 };
 const runtime = { isActiveTaskCompleted: () => false, isTaskTerminal: async () => true, withActiveEnvelope: async (_, fn) => fn(), send: async args => { sent.push(args); return { sent: true }; } };
 const protocol = { startAssignmentActivityObserver: async () => ({stop:async()=>{}}),
  RedisClient: class { async connect(){} close(){} }, taskEvent: (_, event, envelope, extra) => ({ event, ...extra }), eventsKey: () => "events", xaddJson: async () => {},
  readChannelConfig: () => cfg, createRuntime: () => runtime, workflowReminderIsStale: async () => false, isContextOnlyEnvelope: () => true,
  appendLeaderTeamContext: async t => t, appendRedisTeamCompletionGuidance: t => t,
  startConsumer: async (_, fn) => { receive = fn; return { stop: async () => {} }; },
 };
 const native = { admit: () => new Promise(resolve => { release = resolve; }), readLog: async () => 0, api: async resource => ({ data: resource.endsWith("/inbox") ? [] : {} }), interrupt: async () => {} };
 const bridge = await createBridge({ protocol, native, environment: { CLAWMANAGER_GATEWAY_GENERATION: "2", CLAWMANAGER_DEFAULT_PROJECT_PATH: "/workspace/starter" }, log: { error() {} } });
 await bridge.start();
 const tool = async (body, token = bridge.token) => fetch(`http://127.0.0.1:${bridge.port}/tool`, { method: "POST", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: JSON.stringify(body) });
 try {
  assert.equal((await tool({})).status, 409);
  const running = receive({ taskId: "task-1", messageId: "delivery-1", text: "work" });
  await new Promise(resolve => setTimeout(resolve, 10));
  const ids = nativeIdentity({ teamId: "12", memberId: "writer", taskId: "task-1", messageId: "delivery-1", generation: 2 });
  const input = { name: "team_send", callID: "native-call-1", sessionID: ids.session, taskID: ids.message, generation: 2, arguments: { to: "reviewer", text: "review result" } };
  assert.equal((await tool(input, "wrong")).status, 403);
  assert.equal((await tool({ ...input, generation: 1 })).status, 409);
  assert.equal((await tool({ ...input, taskID: "old-input" })).status, 409);
  assert.equal((await tool({ ...input, sessionID: "another-session" })).status, 409);
  assert.equal((await tool(input)).status, 200);
  const repeats = await Promise.all([tool(input), tool(input)]);
  assert.ok(repeats.every(response => response.status === 200));
  assert.equal(sent.length, 1);
  assert.equal((await tool({ ...input, arguments: { to: "reviewer", text: "conflict" } })).status, 409);
  release(); await running;
  assert.equal((await tool(input)).status, 409);
 } finally { await bridge.stop(); }
});

test("completion aliases are normalized before effects and validation errors remain actionable", async () => {
 let receive, release; const delivered = [];
 const cfg = { enabled: true, teamId: "12", memberId: "writer", embeddedTimeoutSeconds: 10 };
 const runtime = {
  isActiveTaskCompleted: () => false, isTaskTerminal: async () => true, withActiveEnvelope: async (_, fn) => fn(),
  completeTask: async args => {
   if (!["succeeded", "failed", "cancelled"].includes(args.status)) throw Error("team_complete_task status must be succeeded, failed or cancelled");
   if (args.summary === "uncertain") throw Error("private-token-canary");
   delivered.push(args); return { decision: "accepted" };
  },
  send: async args => { delivered.push(args); return args; },
  statusContext: async () => ({ status: [{ memberId: "writer" }], workflow: { ledgerVersion: 7, available: true } }),
 };
 const protocol = { startAssignmentActivityObserver: async () => ({stop:async()=>{}}), RedisClient: class { async connect(){} close(){} }, taskEvent: (_, event, envelope, extra) => ({ event, ...extra }), eventsKey: () => "events", xaddJson: async () => {}, readChannelConfig: () => cfg, createRuntime: () => runtime, workflowReminderIsStale: async () => false,
  isContextOnlyEnvelope: () => true, appendLeaderTeamContext: async t => t, appendRedisTeamCompletionGuidance: t => t,
  startConsumer: async (_, fn) => { receive = fn; return { stop: async () => {} }; } };
 const native = { admit: () => new Promise(resolve => { release = resolve; }), readLog: async () => 0,
  api: async resource => ({ data: resource.endsWith("/inbox") ? [] : {} }), interrupt: async () => {} };
 const bridge = await createBridge({ protocol, native, environment: { CLAWMANAGER_GATEWAY_GENERATION: "2" } });
 await bridge.start();
 const running = receive({ taskId: "task-1", messageId: "delivery-1", text: "work" });
 await new Promise(resolve => setTimeout(resolve, 10));
 const ids = nativeIdentity({ teamId: "12", memberId: "writer", taskId: "task-1", messageId: "delivery-1", generation: 2 });
 const call = (id, args, name = "team_complete_task") => fetch(`http://127.0.0.1:${bridge.port}/tool`, {
  method: "POST", headers: { Authorization: `Bearer ${bridge.token}`, "Content-Type": "application/json" },
  body: JSON.stringify({ name, callID: id, sessionID: ids.session, taskID: ids.message, generation: 2, arguments: args }),
 });
 try {
  for (const status of ["waiting", "unknown", ""]) {
   const response = await call("correctable", { status, summary: "ready" });
   assert.equal(response.status, 422);
   assert.equal((await response.json()).error_code, "TEAM_INVALID_COMPLETION");
  }
  assert.equal(delivered.length, 0);
  for (const status of ["completed", "done", "success", "succeeded", "failed", "cancelled"]) {
   const args = { status, summary: "ready", workflowFinal: false };
   assert.equal((await call(status, args)).status, 200);
   assert.equal((await call(status, args)).status, 200);
  }
  assert.equal(delivered.length, 6);
  assert.deepEqual(delivered.map(x => x.status), ["succeeded", "succeeded", "succeeded", "succeeded", "failed", "cancelled"]);
  assert.ok(delivered.every(x => x.workflowFinal === false && !x.taskId && !x.artifactRefs));
  assert.equal((await call("correctable", { status: "done", summary: "ready" })).status, 200);
  const context = await (await call("status", {}, "team_status")).json();
  assert.deepEqual(context.result, [{ memberId: "writer" }]);
  assert.equal(context.workflow.ledgerVersion, 7);
  const send = await (await call("content-alias", { to: "leader", content: "actual message" }, "team_send")).json();
  assert.equal(send.result.text, "actual message");
  const failed = await call("uncertain", { status: "done", summary: "uncertain" });
  assert.equal(failed.status, 500);
  assert.ok(!(await failed.text()).includes("private-token-canary"));
 } finally { release(); await running; await bridge.stop(); }
});
