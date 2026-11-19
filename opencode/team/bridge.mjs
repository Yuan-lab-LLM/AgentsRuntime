import path from "node:path";
import fs from "node:fs/promises";
import { deliveryOutbox } from "./delivery-outbox.mjs";
import { createServer } from "node:http";
import { timingSafeEqual, randomBytes, createHash } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";
import { NativeClient, NativeRequestError, nativeIdentity } from "./native-client.mjs";

const methods = Object.freeze({ team_send: "send", team_status: "status", team_update_progress: "updateProgress", team_complete_task: "completeTask", team_artifact_write: "artifactWrite", team_artifact_read: "artifactRead", team_artifact_list: "artifactList", team_artifact_mkdir: "artifactMkdir", team_artifact_preview: "artifactPreview" });
const equal = (a, b) => typeof a === "string" && Buffer.byteLength(a) === Buffer.byteLength(b) && timingSafeEqual(Buffer.from(a), Buffer.from(b));

// Normalize only an explicit business completion call, never native tool state
// or prose. Validation happens before reserving a call ID or publishing events.
function completionArguments(args) {
 const status = typeof args.status === "string" ? args.status.trim().toLowerCase() : "";
 const canonical = ({ completed: "succeeded", done: "succeeded", success: "succeeded" })[status] || status;
 if (!["succeeded", "failed", "cancelled"].includes(canonical) || typeof args.summary !== "string" || !args.summary.trim()) return null;
 return { ...args, status: canonical };
}

// One bridge belongs to one supervised member process. Protocol/state semantics
// come from the same source as the existing Team adapter.
export async function createBridge({ protocol, native, environment = process.env, log = console, runtimeName = "OpenCode" }) {
 const cfg = protocol.readChannelConfig({});
 const runtime = protocol.createRuntime({ config: {}, logger: log });
 const generation = Number(environment.CLAWMANAGER_GATEWAY_GENERATION);
 // Pro's initial instance generation is zero; Lite Gateway generations start
 // at one. Keep the original generation for governance and fencing.
 const managedPro = environment.CLAWMANAGER_AGENT_ENABLED === "true" && /^[1-9][0-9]*$/.test(environment.INSTANCE_ID || "");
 if (!cfg.enabled || !cfg.teamId || !cfg.memberId || !Number.isSafeInteger(generation) || generation < 0 || generation === 0 && !managedPro) throw new Error(`${runtimeName} Team identity unavailable`);
 const token = randomBytes(32).toString("hex");
 let contextKey=randomBytes(32).toString("hex");
 if(protocol.runtimeStateDir){
  const directory=protocol.runtimeStateDir(cfg),file=path.join(directory,`native-context-${generation}.key`);
  await fs.mkdir(directory,{recursive:true,mode:0o700});
  try{await fs.writeFile(file,contextKey,{flag:"wx",mode:0o600})}catch(error){if(error.code!=="EEXIST")throw error;contextKey=(await fs.readFile(file,"utf8")).trim();if(!/^[a-f0-9]{64}$/.test(contextKey))throw Error("Team context key is invalid")}
 }
 native.contextKey=contextKey;
 let active = null, stopped = false, nativeGone = false, consumer, contextWatcher, controlTimer, controlBusy = false;
 const publishDelivery = async(envelope,payload) => {
  const redis=new protocol.RedisClient(cfg.redisUrl);
  try {await redis.connect();await protocol.xaddJson(redis,protocol.eventsKey(cfg),protocol.taskEvent(cfg,"reply",envelope,payload));}finally{redis.close();}
 };
 const deliveries=protocol.runtimeStateDir ? deliveryOutbox(path.join(protocol.runtimeStateDir(cfg),"native-public-delivery"),publishDelivery) : null;
 const abort = new AbortController();
 let stopInFlight, closeInFlight;
 async function stopExecution() {
  if (stopInFlight) return stopInFlight;
  if (!active) return;
  const current = active;
  stopInFlight = native.interrupt(current.identity).catch(error => { if (!nativeGone) throw error; }).then(() => { current.interrupted = true; }).finally(() => { stopInFlight = null; });
  return stopInFlight;
 }
 const server = createServer(async (request, response) => {
  const reply = (status, value) => { response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" }); response.end(JSON.stringify(value)); };
  if (!equal(request.headers.authorization, `Bearer ${token}`)) return reply(403, { error: "Team tool authorization failed" });
  if (request.method !== "POST" || !["/tool","/context-included"].includes(request.url)) return reply(404, { error: "Unknown Team operation" });
  try {
   let size = 0; const chunks = [];
   for await (const chunk of request) { size += chunk.length; if (size > 1024 * 1024) { reply(413, { error: "Team tool request too large" }); return; } chunks.push(chunk); }
   const input = JSON.parse(Buffer.concat(chunks));
   const current = active;
   if(request.url==="/context-included"){
    if(!current || stopped || input.sessionID!==current.ids.session || input.taskID!==current.ids.message || input.generation!==generation || typeof input.contextMessageId!=="string")return reply(409,{error:"Team context identity is not active"});
    const included=await contextWatcher?.confirm(input.contextMessageId,{nativeSessionId:input.sessionID,nativeMessageId:input.nativeMessageId});
    return reply(200,{ok:!!included});
   }
   if (!current || current.interrupted || stopped || input.sessionID !== current.ids.session || input.taskID !== current.ids.message || input.generation !== generation || !methods[input.name]) return reply(409, { error: "Team execution identity is not active" });
   if (!input.arguments || typeof input.arguments !== "object" || Array.isArray(input.arguments)) return reply(422, { error: "Invalid Team tool arguments" });
   if (typeof input.callID !== "string" || !input.callID || input.callID.length > 512) return reply(422, { error: "Missing native tool call identity" });
   if (input.name === "team_complete_task") {
    const args = completionArguments(input.arguments);
    if (!args) return reply(422, { error_code: "TEAM_INVALID_COMPLETION", error: "Use status=succeeded, failed or cancelled and a non-empty summary. Assignment identity is inherited; a file is not required for a text answer." });
    input.arguments = args;
   }
   if (input.name === "team_send" && !input.arguments.text && !input.arguments.message && typeof input.arguments.content === "string") input.arguments = { ...input.arguments, text: input.arguments.content };
   const method = methods[input.name];
   const digest = createHash("sha256").update(JSON.stringify([input.name, input.arguments])).digest("hex");
   let call = current.calls.get(input.callID);
   if (call && call.digest !== digest) return reply(409, { error: "Conflicting native tool replay" });
   if (!call) {
    if (current.calls.size >= 1024) return reply(409, { error: "Native tool call limit reached" });
    call = { digest, result: Promise.resolve().then(() => method === "status" && runtime.statusContext
     ? runtime.statusContext(input.arguments.memberId)
     : runtime[method](method === "status" ? input.arguments.memberId : input.arguments)) };
    // Retain both successful and uncertain outcomes. A lost HTTP reply must not
    // execute the same native tool call twice. Cross-process business retries
    // remain protected by the authoritative Team protocol's durable receipts.
    current.calls.set(input.callID, call);
   }
   const value = await call.result;
   reply(200, method === "status" && runtime.statusContext
    ? { ok: true, result: value.status, workflow: value.workflow }
    : { ok: true, result: value });
  } catch (error) {
   // These validations occur before a write. Never classify a storage or
   // publication failure as an unexecuted call.
   if (/^(Canonical Team artifact path is outside|Team artifact path escaped|Team artifact scope must|Team-scoped artifact|Only the Team Leader|Invalid Team artifact|Team artifact .*required)/.test(String(error?.message || "")))
    return reply(422, { error_code: "TEAM_INVALID_ARGUMENT" });
   // Runtime/model secrets and source URLs are never returned in tool errors.
   reply(500, { error: "Team tool failed; inspect the current work before retrying" });
  }
 });
 await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });

 async function consume(envelope) {
  if (stopped) throw new Error(`${runtimeName} Team bridge is stopping`);
  if (await protocol.workflowReminderIsStale(cfg, envelope, log)) return;
  const identity = { teamId: String(cfg.teamId), memberId: cfg.memberId, taskId: String(envelope.rootTaskId || envelope.taskId || ""), messageId: String(envelope.messageId || ""), generation };
  const ids = nativeIdentity(identity);
  if (active) throw new Error(`${runtimeName} member already owns an execution`);
  const current = { identity, ids, envelope, interrupted: false, calls: new Map() };
  active = current;
  const startedAt = Date.now();
  let activity, stopHeartbeat = () => {};
  const emitTaskEvent = async (event, extra) => {
   if (event === "assignment_heartbeat" && runtime.isActiveTaskCompleted(envelope.taskId)) return;
   const redis = new protocol.RedisClient(cfg.redisUrl);
   try {
    await redis.connect();
    await protocol.xaddJson(redis, protocol.eventsKey(cfg), protocol.taskEvent(cfg, event, envelope, extra));
   } finally { redis.close(); }
  };
  try {
   let result = await runtime.withActiveEnvelope(envelope, async () => {
    if (!protocol.isContextOnlyEnvelope(envelope)) {
     await protocol.writeLocalStatus(cfg, { availability: "busy", runtimeStatus: "running", currentTaskId: identity.taskId, currentAssignmentId: envelope.assignmentId || envelope.workId, currentWorkId: envelope.workId || envelope.assignmentId, currentSourceMessageId: envelope.messageId, currentRevision: Math.max(1, Number(envelope.revision) || 1), lastSummary: `${runtimeName} Team task started` });
     await emitTaskEvent("task_started", { availability: "busy", runtimeStatus: "running", summary: `${runtimeName} Team task started` });
     stopHeartbeat = protocol.startAssignmentHeartbeat({ envelope, emitTaskEvent, log, isTerminal: () => runtime.isActiveTaskCompleted(envelope.taskId) });

    }
    activity = await protocol.startAssignmentActivityObserver({ cfg, envelope, startedAt, log, observe: () => current.observed, observeContext: true });
    const text = protocol.appendRedisTeamCompletionGuidance(await protocol.appendLeaderTeamContext(String(envelope.text || ""), cfg, envelope), envelope);
    await native.admit(identity, environment.CLAWMANAGER_DEFAULT_PROJECT_PATH || environment.CLAWMANAGER_PROJECT_PATH, [environment.CLAWMANAGER_TEAM_SYSTEM_PROMPT || "", text].filter(Boolean).join("\n\n"), abort.signal);
    await emitTaskEvent("task_progress", { eventKind: "runtime_execution_started", nonAuthoritative: true, stateEffect: "none", rootTaskTerminal: false, visibleToChat: false, nativeInputId: ids.message, activityAssignmentId: envelope.assignmentId || envelope.workId || "root-owner-observation", generation });
    const deadline = Date.now() + cfg.embeddedTimeoutSeconds * 1000;
    let idle = 0, cursor = 0, ownsInput = false;
    while (!current.interrupted) {
     if (Date.now() >= deadline) { await stopExecution(); throw new Error(`${runtimeName} Team execution timed out after confirmed interrupt`); }
     try {
     let nativeFacts;
     if (native.inspectTurn) {
      nativeFacts = await native.inspectTurn(identity,abort.signal);
      current.nativeFailure=nativeFacts.failed===true;
      if(nativeFacts.observed){
       const changed=nativeFacts.observed.sessionCursor && nativeFacts.observed.sessionCursor!==current.observed?.sessionCursor;
       current.observed={...nativeFacts.observed,mtimeMs:changed?Date.now():(current.observed?.mtimeMs||nativeFacts.observed.mtimeMs)};
      }
      if(nativeFacts.publicText && runtime.isActiveTaskCompleted(identity.taskId))current.deliveryText=nativeFacts.publicText;
      if (native.pendingInteraction && (current.observed?.pendingToolName || current.interactionPending) && Date.now() - (current.interactionCheckedAt || 0) >= 10000) {
       current.interactionCheckedAt = Date.now();
       const interaction = await native.pendingInteraction(identity, abort.signal);
       const key = JSON.stringify(interaction);
       if (key !== current.interactionKey && (interaction.pending || current.interactionPending)) {
        await emitTaskEvent("task_progress", { eventKind: interaction.pending ? "runtime_waiting_authorization" : "runtime_authorization_resumed", interactionKinds: interaction.kinds, interactionPending: interaction.pending, nonAuthoritative: true, stateEffect: "none", rootTaskTerminal: false, visibleToChat: interaction.pending, summary: interaction.pending ? "OpenCode 正在等待原生授权或问题回答；原工作保留，不会重复派发。" : "OpenCode 的原生交互等待已结束，继续原工作。" });
       }
       current.interactionKey = key; current.interactionPending = interaction.pending;
      }
     } else cursor = await native.readLog(identity, cursor, event => {
      if (event.type === "session.inbox.delivered") ownsInput = event.data.inboxID === ids.message;
      if (!ownsInput) return;
      // Reasoning content and tool arguments/results never leave the native
      // conversation. Only bounded execution facts inform Team supervision.
      const mtimeMs = Number(event.created);
      const previous = current.observed || {};
      const observed = { ...previous, mtimeMs, lastSessionEventAt: new Date(mtimeMs).toISOString(), sessionCursor: String(event.durable.seq) };
      if (event.type === "session.execution.failed" || event.type === "session.execution.interrupted") current.nativeFailure = true;
      else if (event.type === "session.execution.succeeded") current.nativeFailure = false;
      if (event.type === "session.text.ended") {
       if (runtime.isActiveTaskCompleted(identity.taskId)) if (runtime.isActiveTaskCompleted(identity.taskId)) current.deliveryText = { text: String(event.data.text || ""), recordId: String(event.data.assistantMessageID || ""), at: observed.lastSessionEventAt };
       Object.assign(observed, { lastActivityKind: "assistant", lastAssistantText: String(event.data.text || "").slice(0, 4000), lastAssistantAt: observed.lastSessionEventAt });
      }
      else if (event.type === "session.tool.input.started") Object.assign(observed, { lastActivityKind: "tool_call", pendingToolName: String(event.data.name || "").slice(0, 120) });
      else if (["session.tool.success", "session.tool.failed"].includes(event.type)) Object.assign(observed, { lastActivityKind: "tool_result", lastToolName: previous.pendingToolName || "", pendingToolName: "", lastToolFailed: event.type.endsWith("failed"), lastToolAt: observed.lastSessionEventAt });
      else if (event.type === "session.reasoning.ended") observed.lastActivityKind = "model_output";
      current.observed = observed;
     }, abort.signal);
     const state = await native.api("/api/session/active", { signal: abort.signal });
     const inbox = await native.api(`/api/session/${ids.session}/inbox`, { signal: abort.signal });
     // Never mistake scheduling latency or an input parked in native inbox for
     // a finished turn. Native message persistence confirms input consumption.
     let consumed = nativeFacts?.consumed===true;
     if(!native.inspectTurn) try { await native.api(`/api/session/${ids.session}/message/${ids.message}`, { signal: abort.signal }); consumed = true; }
     catch (error) { if (!(error instanceof NativeRequestError) || error.status !== 404) throw error; }
     if (consumed && !state.data?.[ids.session] && Array.isArray(inbox.data) && !inbox.data.some(item => item.id === ids.message)) idle++; else idle = 0;
     if(current.observationError){
      current.observationError=false;
      await emitTaskEvent("task_progress",{eventKind:"runtime_execution_started",executionObservationRecovered:true,nonAuthoritative:true,stateEffect:"none",rootTaskTerminal:false,visibleToChat:false,nativeInputId:ids.message,generation});
     }
     if (idle >= 2) break;
     } catch(error) {
      if(abort.signal.aborted)throw error;
      idle=0;
      if(!current.observationError){
       current.observationError=true;
       await emitTaskEvent("task_progress",{eventKind:"runtime_reconciliation_needed",failureDomain:"native_observation",retryable:true,executionStopped:false,nativeInputId:ids.message,generation,nonAuthoritative:true,stateEffect:"none",rootTaskTerminal:false,visibleToChat:false,summary:"Native execution observation unavailable; retaining the current execution without replay"}).catch(()=>log.warn?.("Native observation notice remains unconfirmed"));
      }
     }
     await sleep(1000, undefined, { signal: abort.signal });
    }
    if (current.nativeFailure) throw new Error(`${runtimeName} native execution failed after confirmed idle`);
    // Publish only a final public answer attached to this turn's accepted
    // explicit receipt. Intermediate native prose/reasoning stays private.
    if (runtime.isActiveTaskCompleted(identity.taskId) && current.deliveryText?.recordId && current.deliveryText.text.trim()) {
     const body = current.deliveryText;
     const deliveryStatus = await runtime.status(cfg.memberId);
     const deliveryPayload = { eventKind: "delivery_supplement", nativePublicDelivery: true,
      eventId: "native-delivery-" + createHash("sha256").update(JSON.stringify([identity, body.recordId, body.text])).digest("hex"),
      sourceRecordId: body.recordId, sourceMessageId: deliveryStatus?.currentSourceMessageId || envelope.messageId, nativeInputId: ids.message, generation,
      sourceOccurredAt: body.at, lateProjection: true, nonAuthoritative: true, stateEffect: "none",
      rootTaskTerminal: false, text: body.text.slice(0, 64000), textTruncated: body.text.length > 64000,
      summary: "Final public answer from the acknowledged member turn", visibleToChat: true, chatKind: "delivery_supplement" };
     if(deliveries){await deliveries.save(envelope,deliveryPayload); await deliveries.flush().catch(()=>log.warn?.(`${runtimeName} public delivery retained for publication retry`));}
     else await publishDelivery(envelope,deliveryPayload);
    }
    // Native idle is an execution fact, never a business completion receipt.
   }, cfg);
   if (current.interrupted || stopped) throw new Error(`${runtimeName} Team execution stopped; original work is retained`);
   const terminal = await runtime.isTaskTerminal(cfg, envelope);
   if (!terminal) {
    const durable = await protocol.readTurnFacts(cfg, envelope);
    result = protocol.mergeActiveTurnFacts(result, durable);
    if (!result.completed && !result.completionPending) {
     const routing = await protocol.activeMemberRouting(cfg, result.outbound);
     const discovered = routing.currentIsLeader
      ? await protocol.collectRootTaskArtifactRefs(cfg, identity.taskId)
      : await protocol.collectMemberAssignmentArtifactRefs(cfg, identity.taskId, cfg.memberId, envelope.assignmentId || envelope.workId);
     const artifactRefs = await protocol.validateArtifactRefs(cfg, [...(result.artifactRefs || []), ...discovered]);
     const observation = protocol.observeTeamTurnOutcome({ envelope, activeResult: result, durableFacts: durable, terminalAfterDispatch: false, contextOnly: protocol.isContextOnlyEnvelope(envelope), memberRouting: routing });
     const progress = protocol.turnFinishedWithoutCompletionEvent(envelope, { fallbackText: current.observed?.lastAssistantText || "", artifactRefs, hadOutboundAssignment: routing.leaderCoordination, observation });
     await protocol.writeLocalStatus(cfg, { availability: progress.availability, runtimeStatus: progress.runtimeStatus, currentTaskId: identity.taskId, lastSummary: progress.summary });
     await emitTaskEvent("task_progress", progress);
    }
   }
  } finally {
   stopHeartbeat();
   await activity?.stop(current.interrupted ? "cancelled" : current.nativeFailure ? "failed" : "turn_finished").catch(() => log.warn?.(`${runtimeName} Team activity close remains unconfirmed`));
   // If the transport fails, keep identity bound until native execution is
   // proven idle. Do not allow a later delivery to steal its tools.
   if (!current.interrupted) {
    try {
     const state = await native.api("/api/session/active");
     if (state.data?.[ids.session]) await stopExecution();
    } catch (error) {current.recoveryEnvelope=envelope;throw error;}
   }
   if (active === current) active = null;
  }
 }
 return {
  port: server.address().port, token, contextKey,
  async start() {
   const reportFailure = async (envelope,error,stoppedEvidence=false) => {
    log.error(`${runtimeName} Team delivery failed; original work remains available for recovery`);
    if (await runtime.isTaskTerminal(cfg, envelope)) return;
    await protocol.writeLocalStatus(cfg, { availability: "busy", runtimeStatus: "recovering", currentTaskId: envelope.taskId, currentAssignmentId: envelope.assignmentId || envelope.workId, lastSummary: `${runtimeName} Team runtime reconciliation is required` });
    const redis = new protocol.RedisClient(cfg.redisUrl);
    try {
     await redis.connect();
     await protocol.xaddJson(redis, protocol.eventsKey(cfg), protocol.taskEvent(cfg, "task_progress", envelope, { status: "running", availability: "busy", runtimeStatus: "recovering", recoveryFaultKey: envelope.metadata?.recoveryFaultKey, boundedRecoveryAttempt: envelope.metadata?.boundedRecoveryAttempt, executionStopped: stoppedEvidence || !active, generation, failureCode: /native request failed/.test(String(error)) ? "native_transport_failure" : "native_execution_failure", eventKind: "runtime_reconciliation_needed", failureDomain: "runtime_adapter", retryable: true, stateEffect: "none", nonAuthoritative: true, rootTaskTerminal: false, visibleToChat: false, chatPolicy: "hidden", summary: `${runtimeName} Team runtime reconciliation is required` }));
    } finally { redis.close(); }
   };
   await deliveries?.flush().catch(()=>log.warn?.(`${runtimeName} public delivery remains pending`));
   consumer = await protocol.startConsumer(cfg, consume, reportFailure, log);
   if(protocol.startActiveContextWatcher && typeof native.steer==="function")contextWatcher=await protocol.startActiveContextWatcher(cfg,{
    getActive:()=>!stopped && active && !active.interrupted && !runtime.isActiveTaskCompleted(active.identity.taskId)?{envelope:active.envelope,generation,identity:active.identity}:null,
    deliver:(notice,text,current)=>native.steer(current.identity,text,notice.messageId,abort.signal),
    observe:(record,current)=>native.observeContext?.(record,current.identity),
   },log);
   // This control probe never runs a model. A failed HTTP read cannot leave
   // the member's identity pinned forever after its process actually exited.
   controlTimer=setInterval(async()=>{
    const held=active;
    if(stopped || controlBusy)return;
    controlBusy=true;
    try {
     await deliveries?.flush();
     if(!held?.recoveryEnvelope)return;
     const state=await native.api("/api/session/active");
     const inbox=await native.api(`/api/session/${held.ids.session}/inbox`);
     if(!state.data?.[held.ids.session] && Array.isArray(inbox.data) && inbox.data.length===0 && active===held){
      await reportFailure(held.recoveryEnvelope,"native transport recovered; original execution confirmed idle",true);
      if(active===held) active=null;
     }
    } catch {} finally {controlBusy=false}
   },5000);
   controlTimer.unref?.();
   if (!consumer) throw new Error(`${runtimeName} Team consumer did not start`);
  },
  async stop({ nativeExited = false } = {}) {
   if (nativeExited) nativeGone = true;
   if (nativeExited && active) active.interrupted = true;
   if (closeInFlight) return closeInFlight;
   stopped = true;
   clearInterval(controlTimer);
   closeInFlight = (async () => {
    await contextWatcher?.stop();
    // Stop admission first. The accepted native execution is then interrupted
    // and joined; untouched inbox entries remain pending for the next process.
    await consumer?.stop({ waitForExecution: false });
    if (nativeExited && active) active.interrupted = true;
    else await stopExecution();
    abort.abort();
    await consumer?.stop();
    await new Promise(resolve => server.close(resolve));
   })().catch(error => { closeInFlight = null; throw error; });
   return closeInFlight;
  },
 };
}

export { NativeClient };
