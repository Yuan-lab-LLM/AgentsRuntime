import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const redisUrl = process.env.REDIS_TEAM_TEST_URL;
if (!redisUrl) {
  throw new Error("REDIS_TEAM_TEST_URL is required");
}

const root = await fs.mkdtemp(path.join(os.tmpdir(), "redis-team-consumer-"));
const state = path.join(root, "state");
const shared = path.join(root, "shared");
process.env.XDG_STATE_HOME = state;

const distPath = path.resolve(import.meta.dirname, "..", "dist", "index.js");
const source = (await fs.readFile(distPath, "utf8"))
  .replace(
    'import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";',
    "const definePluginEntry = (entry) => entry;",
  )
  ;
const testSource = source + "\nexport { RedisClient, completionAckKey, completionKey, createTurnAnswerCapture };\n";
const pluginModule = await import(
  `data:text/javascript;base64,${Buffer.from(testSource).toString("base64")}`
);

const teamId = "consumer-regression";
const memberId = "developer";
const inboxKey = `claw:team:${teamId}:inbox:${memberId}`;
const eventsKey = `claw:team:${teamId}:events`;
const presenceKey = `claw:team:${teamId}:presence`;
const dlqKey = `claw:team:${teamId}:dlq`;
const runId = String(Date.now());
const config = {
  channels: {
    "redis-team": {
      accounts: {
        default: {
          fromEnv: false,
          enabled: true,
          redisUrl,
          teamId,
          memberId,
          role: "developer",
          sharedDir: shared,
          autoRun: true,
          consumerGroup: "consumer-regression-test",
          inboxKey,
          eventsKey,
          presenceKey,
          dlqKey,
        },
      },
    },
  },
};

const sessionResult = "开发任务已完成，产物已经保存并可交给 Reviewer 验收。";
const callbackAnswer = "本轮完整答案：来自十二个已核对的资讯来源，不需要额外文件。";
const capture = pluginModule.createTurnAnswerCapture();
capture.capture({text:callbackAnswer});
capture.capture({text:'private reasoning',isReasoning:true});
capture.capture({text:'NO_REPLY'});
capture.close();
capture.capture({text:'late other turn'});
assert.equal(capture.text,callbackAnswer);
let dispatchMode = "assignment";
const hookHandlers = new Map();
async function dispatchThroughHooks() {
  const timestamp = new Date().toISOString();
  const records = dispatchMode === "context-retryable" ? [
    {
      id: "assistant-context-tool-call",
      timestamp,
      message: {
        role: "assistant",
        content: [{ type: "tool_use", id: "tool-context-1", name: "team_send", input: {} }],
      },
    },
    {
      id: "context-tool-result",
      timestamp,
      message: {
        role: "tool",
        content: [{
          type: "tool_result",
          tool_use_id: "tool-context-1",
          text: JSON.stringify({ ok: false, retryable: true, code: "ambiguous_team_target", candidates: ["developer", "reviewer"] }),
        }],
      },
    },
    {
      id: "assistant-context-final",
      timestamp,
      message: { role: "assistant", content: [{ type: "text", text: "I will correct the Team recipient." }] },
    },
  ] : [
    {
      id: "assistant-tool-call",
      timestamp,
      message: {
        role: "assistant",
        content: [{ type: "tool_use", id: "tool-1", name: "browser", input: {} }],
      },
    },
    {
      id: "failed-tool-result",
      timestamp,
      message: {
        role: "tool",
        content: [{ type: "tool_result", tool_use_id: "tool-1", isError: true, text: "evaluation syntax error" }],
      },
    },
    {
      id: "assistant-final",
      timestamp,
      message: { role: "assistant", content: [{ type: "text", text: sessionResult }] },
    },
  ];
  const hookContext = { sessionKey: "consumer-regression-session" };
  for (const record of records) {
    const role = String(record?.message?.role || "").toLowerCase();
    if (role === "assistant") {
      await hookHandlers.get("before_message_write")?.(record, hookContext);
      for (const part of Array.isArray(record.message.content) ? record.message.content : []) {
        if (!["tool_use", "tool_call"].includes(part?.type)) continue;
        await hookHandlers.get("before_tool_call")?.({
          toolName: part.name,
          toolCallId: part.id,
        }, hookContext);
      }
    }
    if (role === "tool") {
      const result = record.message.content?.[0] || {};
      let structuredResult = {};
      try { structuredResult = JSON.parse(result.text || "{}"); } catch {}
      await hookHandlers.get("after_tool_call")?.({
        toolName: dispatchMode === "context-retryable" ? "team_send" : "browser",
        toolCallId: result.tool_use_id,
        isError: result.isError === true,
        result: structuredResult,
        error: result.isError === true ? { code: "tool_failed" } : undefined,
      }, hookContext);
    }
  }
  return {};
}

let registeredChannel;
pluginModule.default.register({
  config,
  logger: { info() {}, warn() {}, error() {} },
  registerTool() {},
  on(name, handler) { hookHandlers.set(name, handler); },
  registerChannel(channel) {
    registeredChannel = channel.plugin;
  },
});
assert.ok(registeredChannel?.gateway?.startAccount, "Redis Team channel gateway must be registered");

const redis = new pluginModule.RedisClient(redisUrl);
const abortController = new AbortController();
const statuses = [];
const logs = [];
const context = {
  accountId: "default",
  cfg: config,
  channelRuntime: {
    routing: {
      resolveAgentRoute({ accountId, peer }) {
        return {
          accountId,
          agentId: "main",
          sessionKey: `agent:main:redis-team:group:${peer.id}`,
        };
      },
    },
    inbound: {
      async buildContext(payload) { return payload; },
      async run({ adapter }) {
        const turn = adapter.resolveTurn();
        assert.equal(turn.ctxPayload.conversation.kind, "group");
        assert.equal(turn.ctxPayload.access.commands.authorized, false);
        if (dispatchMode === "callback-only") {
          await turn.delivery.deliverWithProviderMessageSending({text:callbackAnswer});
          return {};
        }
        await dispatchThroughHooks();
        return {};
      },
    },
  },
  abortSignal: abortController.signal,
  setStatus(status) {
    statuses.push(status);
  },
  log: {
    info(message) { logs.push({ level: "info", message: String(message) }); },
    warn(message) { logs.push({ level: "warn", message: String(message) }); },
    error(message) { logs.push({ level: "error", message: String(message) }); },
  },
};

function streamPayloads(response) {
  if (!Array.isArray(response)) return [];
  const payloads = [];
  for (const item of response) {
    if (!Array.isArray(item) || !Array.isArray(item[1])) continue;
    const fields = item[1];
    for (let index = 0; index < fields.length; index += 2) {
      if (fields[index] !== "payload") continue;
      try {
        payloads.push(JSON.parse(fields[index + 1]));
      } catch {}
    }
  }
  return payloads;
}

async function waitFor(check, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const result = await check();
    if (result) return result;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("timed out waiting for Redis Team consumer regression condition");
}

let gatewayPromise;
try {
  await fs.mkdir(shared, { recursive: true });
  await fs.writeFile(
    path.join(shared, "team.json"),
    JSON.stringify({
      teamId,
      communicationMode: "leader_mediated",
      members: [
        { memberId: "delivery-lead", role: "leader", isLeader: true },
        { memberId, role: "developer" },
      ],
    }),
    "utf8",
  );
  await redis.connect();
  await redis.command("DEL", inboxKey, eventsKey, presenceKey, dlqKey, `claw:team:${teamId}:maintenance`);

  gatewayPromise = registeredChannel.gateway.startAccount(context);
  await waitFor(() => statuses.some((status) => status.connected === true));

  const envelope = {
    v: 4,
    protocolVersion: 4,
    messageId: `consumer-regression-message-${runId}`,
    idempotencyKey: `consumer-regression-message-${runId}`,
    taskId: `team-consumer-regression-task-${runId}`,
    rootTaskId: `team-consumer-regression-task-${runId}`,
    rootMessageId: "root-message-1",
    assignmentId: "dev-implementation",
    workId: "dev-implementation",
    revision: 1,
    teamId,
    from: "delivery-lead",
    to: memberId,
    role: "leader",
    type: "assignment",
    intent: "assignment",
    text: "实现页面并把结果交给 Reviewer。",
    requiresCompletion: true,
    responseLocale: "zh-CN",
    createdAt: new Date().toISOString(),
  };
  await redis.command("XADD", inboxKey, "*", "payload", JSON.stringify(envelope));

  const observed = [];
  const turnFinished = await waitFor(async () => {
    const response = await redis.command("XRANGE", eventsKey, "-", "+");
    observed.splice(0, observed.length, ...streamPayloads(response));
    return observed.find((event) => event.eventKind === "turn_finished_without_completion") || false;
  });

  assert.ok(turnFinished, "a completed assistant turn without a receipt must emit Monitor evidence");
  assert.equal(turnFinished.activeTurnFinished, true);
  assert.equal(turnFinished.lastToolFailed, true, "failed tool evidence must remain visible in the turn audit");
  assert.equal(turnFinished.stateEffect, "none");
  assert.equal(turnFinished.rootTaskTerminal, false, "turn prose must not finish the root task");
  assert.equal(turnFinished.resultMarkdown, sessionResult);
  assert.equal(
    observed.some((event) => event.event === "completion_proposed"),
    false,
    "natural assistant prose must never become a completion proposal",
  );
  assert.equal(
    observed.some((event) => event.event === "task_failed" || event.type === "task_failed"),
    false,
    "a Runtime reconciliation path must not convert completed work into a business failure",
  );
  assert.equal(
    observed.some((event) => event.visibleToChat !== false && event.text === "Redis Team task completed"),
    false,
    "the Runtime completion control placeholder must never be projected into Team chat",
  );
	assert.equal(
		observed.some((event) => event.eventKind === "agent_narrative"),
		false,
		"internal assistant session prose must not be projected into the Team event stream",
	);
  assert.equal(
    await redis.command("XLEN", dlqKey),
    0,
    "a valid turn-end monitor record must not enter the dead-letter stream",
  );

  // Exercise the channel's real delivery callback, not a prefilled hook record
  // or direct completion tool invocation. Both assignment kinds share capture.
  dispatchMode = "callback-only";
  for (const single of [false,true]) {
    const messageId=`callback-${single}-${runId}`;
    const rootTaskId=`team-${teamId}-task-${single ? 'single' : 'ordinary'}-${runId}`;
    const callbackEnvelope={...envelope,messageId,idempotencyKey:messageId,taskId:rootTaskId,rootTaskId,rootMessageId:messageId,
      ...(single ? {from:'clawmanager',intent:'run_task',executionType:'single_member',executorMemberKey:memberId,assignmentId:'single-member',workId:'single-member'} : {})};
    await redis.command('XADD',inboxKey,'*','payload',JSON.stringify(callbackEnvelope));
    const end=await waitFor(async()=>streamPayloads(await redis.command('XRANGE',eventsKey,'-','+')).find(e=>e.eventKind==='turn_finished_without_completion' && (e.messageId===messageId || e.sourceMessageId===messageId)));
    assert.equal(end.resultMarkdown,callbackAnswer);
    assert.equal(end.hadAssistantNarrative,true);
    assert.equal(end.immediateRecoveryEligible,true);
    assert.equal(end.turnObservationOutcome,'completion_receipt_gap');
    assert.equal(end.visibleToChat,false);
    assert.equal(end.rootTaskTerminal,false);
  }

  dispatchMode = "context-retryable";
  const contextMessageId = `consumer-context-${runId}`;
  const contextEnvelope = {
    ...envelope,
    messageId: contextMessageId,
    idempotencyKey: contextMessageId,
    intent: "member_result_confirmed",
    type: "notification",
    text: "A member result was confirmed. Continue the current workflow decision.",
    requiresCompletion: false,
    turnOutcomePolicy: {
      actionExpected: true,
      immediateRecoveryAllowed: true,
      reason: "consumer_regression",
    },
    createdAt: new Date().toISOString(),
  };
  const maintenanceKey = `claw:team:${teamId}:maintenance`;
  await redis.command("SET", maintenanceKey, JSON.stringify({ enabled: true, rolloutId: "rollout-contract" }));
  await redis.command("XADD", inboxKey, "*", "payload", JSON.stringify(contextEnvelope));
  await new Promise((resolve) => setTimeout(resolve, 1500));
  const whileMaintained = streamPayloads(await redis.command("XRANGE", eventsKey, "-", "+"));
  assert.equal(
    whileMaintained.some((event) => event.sourceMessageId === contextMessageId || event.messageId === contextMessageId),
    false,
    "maintenance drain must finish the current turn but must not accept a new Team message",
  );
  await redis.command("DEL", maintenanceKey);
  const contextTurn = await waitFor(async () => {
    const response = await redis.command("XRANGE", eventsKey, "-", "+");
    observed.splice(0, observed.length, ...streamPayloads(response));
    return observed.find((event) =>
      event.eventKind === "turn_finished_without_completion" &&
      (event.sourceMessageId === contextMessageId || event.messageId === contextMessageId)) || false;
  });
  assert.equal(contextTurn.turnObservationOutcome, "retryable_tool_gap");
  assert.equal(contextTurn.immediateRecoveryEligible, true);
  assert.equal(contextTurn.contextOnlyTurn, true);
  assert.equal(contextTurn.lastToolName, "team_send");
  assert.equal(contextTurn.lastToolCode, "ambiguous_team_target");
  assert.deepEqual(contextTurn.targetCandidates, ["developer", "reviewer"]);
  assert.equal(contextTurn.stateEffect, "none");
  assert.equal(contextTurn.rootTaskTerminal, false);
  assert.equal(
    observed.some((event) =>
      (event.sourceMessageId === contextMessageId || event.messageId === contextMessageId) &&
      ["task_received", "task_started", "completion_proposed", "task_failed"].includes(event.event)),
    false,
    "context observation must not create business lifecycle state",
  );

  console.log("Redis Team real consumer regression: OK");
} finally {
  abortController.abort();
  await gatewayPromise?.catch(() => {});
  redis.close();
  await fs.rm(root, { recursive: true, force: true });
}
