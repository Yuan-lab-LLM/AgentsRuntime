import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const required = ["package.json", "openclaw.plugin.json", "dist/index.js", "README.md"];
for (const rel of required) {
  const full = path.join(root, rel);
  if (!fs.existsSync(full)) throw new Error(`missing required file: ${rel}`);
}
const manifest = JSON.parse(fs.readFileSync(path.join(root, "openclaw.plugin.json"), "utf8"));
const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
const dist = fs.readFileSync(path.join(root, "dist", "index.js"), "utf8");
if (manifest.id !== "redis-team") throw new Error(`unexpected plugin id: ${manifest.id}`);
if (pkg.version !== "0.2.2") {
  throw new Error(`unexpected package version: ${pkg.version}`);
}
if (pkg.openclaw?.compat?.pluginApi !== ">=2026.5.4") {
  throw new Error("package.json must preserve the OpenClaw 2026.5.4 plugin API compatibility floor");
}
if (pkg.openclaw?.build?.openclawVersion !== "2026.7.1-2") {
  throw new Error("package.json must declare the tested OpenClaw 2026.7.1-2 build baseline");
}
if (!pkg.openclaw?.extensions?.includes("./dist/index.js")) {
  throw new Error("package.json openclaw.extensions must include ./dist/index.js");
}
for (const token of [
  "CLAWMANAGER_TEAM_INBOX_KEY",
  "CLAWMANAGER_TEAM_EVENTS_KEY",
  "CLAWMANAGER_TEAM_PRESENCE_KEY",
  "CLAWMANAGER_TEAM_DLQ_KEY",
  "task_received",
  "task_started",
  "runtimeStatus",
  "availability",
]) {
  if (!dist.includes(token)) throw new Error(`dist/index.js missing Redis Team protocol token: ${token}`);
}
for (const token of [
  "completeActiveTask",
  "failActiveTask",
  "xaddTerminalOnce",
  "completionKey",
  "processedMessageKey",
  "stableAssignmentId",
  "validateArtifactRefs",
  "isActiveCompletionTarget",
  "taskIdAliases",
  "writeTaskEnvelope",
  "readTaskEnvelope",
  "runtimeStateDir",
  "privateTaskEnvelopePath",
  "privateActiveAssignmentPath",
  "writeActiveAssignmentEnvelope",
  "readActiveAssignmentEnvelope",
  "markActiveAssignmentTerminal",
  "ACTIVE_ASSIGNMENT_LEASE_MS",
  "writeJsonBestEffort",
  "TEAM_SHARED_DIR_MODE = 0o2775",
  "isTaskTerminal",
  "statusIsActive",
  "pendingDrainBatches",
  "pending/history drain limit reached",
  "waitForConsumerStop",
  "resolveConsumerStopped",
  "targetResolver",
  "inferTargetChatType",
  "baseSessionKey",
  "completionMessageId",
  "completionId",
  "completionSource",
  "explicitCompletion",
  "WIRE_SCHEMA_VERSION = 1",
  "PROTOCOL_VERSION = 4",
  "CONTROL_PLANE_REPLY_TARGETS",
  "completion_proposed",
  "waitForCompletionAcknowledgement",
  "waitForTerminalCompletionState",
  "completion-state:",
  "artifact_changed",
  "waivers",
  "skippedAssignments",
  "phaseDispositions",
  "explicit-disposition-v1",
  "completion_pending",
  "awaiting_completion_receipt",
  "resultMarkdown",
  "Math.min(99",
  "await writeText(resultMarkdownPath, resultMarkdown)",
  "message_failed",
  "assignment-",
  "team_artifact_write",
  "team_artifact_read",
  "team_artifact_preview",
  "team_artifact_list",
  "team_artifact_mkdir",
  "assertNoArtifactSymlinkTraversal",
  "assertTeamArtifactWriteScope",
  "assertResponseLocale",
  "sharedWorkspaceForTarget",
  "artifactRootTaskId",
  "collectRootTaskArtifactRefs",
  "kind=plan, kind=context, kind=review, or kind=final",
  "assignment_activity_v1",
  "startAssignmentActivityObserver",
  "Authoritative assignment artifact canonical root:",
  "Shared research rule:",
  "Current-root shared work physical root:",
  "taskWorkPhysicalRoot",
  "canonicalTeamArtifactRefsFromText",
  "artifactMetadataForRefs",
  "teamResultContentHash",
  "resultContentHash",
  "reviewedArtifactRefs",
  "analyzeResponseLocale",
  "workflowReminderIsStale",
  "rootWorkflowStateKey",
  'stateEffect: "none"',
  "truncated:",
  "nextOffset:",
  "assistant_session",
  "readAssistantNarrativesFromDispatch",
  "sourceOccurredAt",
  "lateProjection",
  "suppressedAfterTerminal",
  "terminalDelivery",
  "semanticEventKind",
  "leader-final-synthesis",
  "Available Team artifact references:",
  "already_terminal",
  "suppressed duplicate reply after submitted completion",
	"before_message_write",
	"withNarrativeProjection",
	"durableTurnFacts.completionProposed",
	"browserVerification",
	"mergeBrowserVerificationState",
	"Assignment-specific ownership:",
	"call team_complete_task once",
  "resolveRedisTeamVerificationRole",
  "Evidence verification policy:",
  "Code review policy:",
  "API verification policy:",
  "reviewerBrowserToolDecision",
  "reviewerBrowserGuardKey",
  "browserToolCallFailed",
  "Team artifact Browser preview",
  "single brief Browser verification budget is exhausted",
  "ignored late assignment for terminal root before Agent dispatch",
  "reviewVerdict",
]) {
  if (!dist.includes(token)) throw new Error(`dist/index.js missing Redis Team completion token: ${token}`);
}
if (dist.includes("params.taskId === activeEnvelope.taskId")) {
  throw new Error("dist/index.js must match active Redis Team task ids through aliases");
}
if (dist.includes("browserVerification=unavailable")) {
  throw new Error("Reviewer guidance must not expose browserVerification=unavailable as a user-facing verdict");
}
const verificationRoleResolverStart = dist.indexOf("function resolveRedisTeamVerificationRole");
const verificationRoleResolverEnd = dist.indexOf("function redisTeamVerificationGuidance", verificationRoleResolverStart);
if (verificationRoleResolverStart < 0 || verificationRoleResolverEnd < 0) {
  throw new Error("unable to locate validation role resolver");
}
const verificationRoleResolver = dist.slice(verificationRoleResolverStart, verificationRoleResolverEnd);
for (const forbiddenRolePattern of [
  'role.includes("review")',
  'role.includes("qa")',
  'profileKey.includes("review")',
]) {
  if (verificationRoleResolver.includes(forbiddenRolePattern)) {
    throw new Error(`validation role matching must use exact aliases, found: ${forbiddenRolePattern}`);
  }
}
if (dist.includes('path.join(cfg.sharedDir, ".openclaw-redis-team", "tasks"')) {
  throw new Error("member-scoped Redis Team envelopes must not require a shared NFS .openclaw-redis-team/tasks directory");
}
if (dist.includes('|| "unscoped"')) {
  throw new Error("member Team artifacts must reject missing root task context instead of writing an unscoped path");
}
const validationWriterHelper = dist.indexOf("function isAssignedValidationWriter");
const validationWriterGuard = dist.indexOf('kind === "review" && isAssignedValidationWriter(cfg, activeEnvelope)');
const genericTeamScopeRejection = dist.indexOf("Only the Team Leader or assigned validator may write this team-scoped artifact");
if (
  validationWriterHelper < 0 ||
  validationWriterGuard < 0 ||
  genericTeamScopeRejection < 0 ||
  validationWriterHelper > validationWriterGuard ||
  validationWriterGuard > genericTeamScopeRejection
) {
  throw new Error("Assigned validation publishing must be handled by the team-scope guard");
}
const deliverStart = dist.indexOf("deliver: async (payload) => {");
const deliverEnd = dist.indexOf("onRecordError:", deliverStart);
if (deliverStart < 0 || deliverEnd < 0) throw new Error("unable to locate Redis Team deliver callback");
const deliverBody = dist.slice(deliverStart, deliverEnd);
if (deliverBody.includes("completeActiveTask") || deliverBody.includes('"task_completed"')) {
  throw new Error("normal Redis Team replies must not complete the business task");
}
if (!deliverBody.includes("runtime.isActiveTaskCompleted")) {
  throw new Error("normal Redis Team replies must be suppressed after explicit completion");
}
for (const tool of ["team_artifact_write", "team_artifact_read", "team_artifact_preview", "team_artifact_list", "team_artifact_mkdir"]) {
  if (!manifest.contracts?.tools?.includes(tool)) {
    throw new Error(`openclaw.plugin.json missing tool contract: ${tool}`);
  }
}
if (dist.includes("seenMessageIds")) {
  throw new Error("dist/index.js must use Redis-backed message idempotency instead of process memory");
}
if (!dist.includes('const runtimeStatus = "completion_pending"')) {
  throw new Error("dist/index.js must wait for backend acknowledgement before marking Redis Team tasks terminal");
}
console.log("openclaw-redis-team build check passed");
