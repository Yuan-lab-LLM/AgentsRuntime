import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

if (!process.env.REDIS_TEAM_TEST_URL) throw Error("An isolated REDIS_TEAM_TEST_URL is required");
const root = await fs.mkdtemp(path.join(os.tmpdir(), "team-workflow-context-"));
process.env.XDG_STATE_HOME = path.join(root, "state");
const filename = process.env.TEAM_PROTOCOL_SOURCE || path.resolve(import.meta.dirname, "../dist/index.js");
const source = (await fs.readFile(filename, "utf8"))
 .replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";', "const definePluginEntry = x => x;")
 .replace('import { dispatchInboundDirectDmWithRuntime } from "openclaw/plugin-sdk/channel-inbound";', "const dispatchInboundDirectDmWithRuntime = async () => ({});");
const mod = await import(`data:text/javascript;base64,${Buffer.from(source + "\nexport { createRuntime, RedisClient, coalescedWorkflowReminders, appendLeaderTeamContext, workflowReminderIsStale };\n").toString("base64")}`);
const cfg = { fromEnv: false, enabled: true, redisUrl: process.env.REDIS_TEAM_TEST_URL, teamId: "501", memberId: "leader", role: "leader", sharedDir: root };
const runtime = mod.createRuntime({ config: { channels: { "redis-team": { accounts: { default: cfg } } } } });
const redis = new mod.RedisClient(cfg.redisUrl);
await redis.connect();
const key = "claw:team:501:root:team-501-task-1:state";
const envelope = { teamId: "501", taskId: "team-501-task-1", rootTaskId: "team-501-task-1", messageId: "root-one", to: "leader" };
try {
 await fs.writeFile(path.join(root, "team.json"), JSON.stringify({ teamId: "501", leaderMemberId: "leader", members: [{ memberId: "leader", role: "leader", isLeader: true }] }));
 await runtime.withActiveEnvelope(envelope, async () => {
  assert.equal((await runtime.statusContext()).workflow.available, false);
  await redis.command("SET", key, JSON.stringify({ teamId: 501, rootTaskId: envelope.taskId, ledgerVersion: 7, planVersion: 1, workflowState: "awaiting_leader_decision", assignmentLedgerComplete: true,
   assignments: { work: { assignmentId: "work", revision: 1, status: "succeeded", hasAcceptedResult: true, acceptedResult: { summary: "done", resultMarkdown: "Actual role answer", rawSecret: "private-canary" } } }, rawSecret: "private-canary" }));
  const first = await runtime.statusContext();
  assert.ok(Array.isArray(first.status));
  assert.equal(first.workflow.assignments.work.acceptedResult.resultMarkdown, "Actual role answer");
  assert.ok(!JSON.stringify(first).includes("private-canary"));
  const text = await mod.appendLeaderTeamContext("old ledger=6", cfg, { ...envelope, requiresCompletion: false, intent: "leader_workflow_decision", assignmentId: "leader-final-synthesis" });
  assert.ok(text.includes('"ledgerVersion":7') && text.includes("Actual role answer"));
  await redis.command("SET", key, JSON.stringify({ teamId: 999, rootTaskId: envelope.taskId, ledgerVersion: 8 }));
  assert.equal((await runtime.statusContext()).workflow.available, false);
 });
 const msg = (id, ledger = 7, intent = "leader_workflow_decision") => ({ ...envelope, redisId: id, messageId: id, from: "clawmanager-monitor", requiresCompletion: false, intent, planVersion: 1, ledgerVersion: ledger, metadata: { resultFingerprint: "results-seven" } });
 const result = mod.coalescedWorkflowReminders([msg("1-0"), msg("2-0"), msg("3-0", 8), msg("4-0", 7, "assignment")]);
 assert.deepEqual([...result], ["1-0"]);
 // Member-result and legacy recovery notices are context too. After a cold
 // restart they must not launch a new model turn for an already closed root.
 const late = { ...envelope, requiresCompletion: false, intent: "member_result_confirmed", messageId: "late-result" };
 await redis.command("SET", key, JSON.stringify({ rootTaskId: envelope.taskId, status: "failed", terminal: true }));
 assert.equal(await mod.workflowReminderIsStale(cfg, late, { info() {} }), true);
 assert.equal(await mod.workflowReminderIsStale(cfg, { ...late, taskId: "team-501-task-2", rootTaskId: "team-501-task-2" }, { info() {} }), false);
 await redis.command("SET", key, JSON.stringify({ rootTaskId: envelope.taskId, status: "running", terminal: false }));
 assert.equal(await mod.workflowReminderIsStale(cfg, late, { info() {} }), false);
 assert.equal(await mod.workflowReminderIsStale(cfg, { ...envelope, intent: "assignment" }, { info() {} }), false);
 console.log("Current workflow, missing/foreign projections and reminder coalescing: PASS");
} finally { await redis.command("DEL", key); redis.close(); await fs.rm(root, { recursive: true, force: true }); }
