import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const distPath = path.resolve(import.meta.dirname, "..", "dist", "index.js");
const source = (await fs.readFile(distPath, "utf8"))
  .replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";', 'const definePluginEntry = (entry) => entry;')
  .replace('import { dispatchInboundDirectDmWithRuntime } from "openclaw/plugin-sdk/direct-dm";', 'const dispatchInboundDirectDmWithRuntime = async () => ({});');
const testSource = source + "\nexport { createRuntime, normalizeEnvelope, normalizePhaseDispositions, appendRedisTeamCompletionGuidance, appendLeaderTeamContext, turnFinishedWithoutCompletionEvent, assignmentAttemptFailedEvent, isIncompleteTurnDelivery, activeMemberRouting, mergeActiveTurnFacts, normalizeRedisTeamTarget, resolveRedisTeamTarget, resolveRosterIdentity, decideBusinessDelivery, normalizeTeamSendParams, canonicalArtifactAlias, canonicalTeamArtifactRefsFromText, inferCanonicalArtifactWriteContract, mergeTaskEnvelopeArtifactContext, sharedWorkspaceForTarget, lateNarrativeProjectionMeta, normalizeAssistantSessionText, assistantSessionNarrativesForProjection, verificationTargetUrl, reviewerBrowserToolDecision, reviewerBrowserToolResultDecision, reviewerBrowserGuardKey, browserVerificationForCompletion, mergeBrowserVerificationState, browserToolCallFailed, browserToolResultUrl, teamProcessToolDecision, assignmentHasIndependentReview, rootWorkflowStateIsTerminal, previewUrlForTeamArtifact, sessionToolOutcome, readLastToolOutcomeFromDispatch, readTurnToolEvidenceFromDispatch, observeTeamTurnOutcome, contextTurnOutcomePolicy, latestRuntimeSessionActivity, completionProposalProvenance, validationRevisionDirectory, equivalentActiveAssignment, equivalentWorkflowAttempt };\n";
const pluginModule = await import(`data:text/javascript;base64,${Buffer.from(testSource).toString("base64")}`);
const plugin = pluginModule.default;

const root = await fs.mkdtemp(path.join(os.tmpdir(), "redis-team-75-"));
const state = path.join(root, "state");
const shared = path.join(root, "shared");
process.env.XDG_STATE_HOME = state;
process.env.CLAWMANAGER_TEAM_TOKEN = "team-75-preview-secret";
process.env.CLAWMANAGER_TEAM_ID = "75";
process.env.CLAWMANAGER_TEAM_PREVIEW_ORIGIN = "http://clawmanager-egress-proxy.clawmanager-hxc-peer-system.svc.cluster.local:3128";
process.env.CLAWMANAGER_BROWSER_PROXY_URL = "http://clawmanager-egress-proxy.clawmanager-hxc-peer-system.svc.cluster.local:3128";

function createHarness(memberId, role) {
  const registered = new Map();
  const hooks = new Map();
  const config = {
    channels: {
      "redis-team": {
        accounts: {
          default: {
            fromEnv: false,
            enabled: true,
            teamId: "75",
            memberId,
            role,
            sharedDir: shared,
          },
        },
      },
    },
  };
  plugin.register({
    config,
    registerTool(tool) {
      registered.set(tool.name, tool);
    },
    on(name, handler) {
      if (!hooks.has(name)) hooks.set(name, []);
      hooks.get(name).push(handler);
    },
    registerChannel() {},
  });
  registered.hooks = hooks;
  return registered;
}

async function seedActive(memberId, role, assignmentId, extra = {}) {
  const dir = path.join(state, "teams", "75", memberId);
  await fs.mkdir(dir, { recursive: true });
  const envelope = {
    teamId: "75",
    memberId,
    role,
    taskId: "team-75-task-150",
    rootTaskId: "team-75-task-150",
    messageId: `msg-${memberId}`,
    rootMessageId: "team-75-task-1784165223585610285",
    assignmentId,
    workId: assignmentId,
    phaseId: role === "reviewer" ? "phase-review" : "phase-dev",
    responseLocale: "zh-CN",
    activeAssignmentContext: {
      teamId: "75",
      memberId,
      recordedAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      terminal: false,
    },
    ...extra,
  };
  await fs.writeFile(path.join(dir, "active-assignment.json"), JSON.stringify(envelope), "utf8");
}

function toolResult(result) {
  return JSON.parse(result.content[0].text);
}

function resultContentHash(content, refs) {
  const normalized = String(content || "").trim().split(/\s+/).filter(Boolean).join(" ");
  return "sha256:" + createHash("sha256")
    .update(normalized + "\nrefs=" + [...refs].sort().join("|"))
    .digest("hex");
}

try {
  await fs.mkdir(shared, { recursive: true });
	const hookHarness = createHarness("hook-member", "developer");
	const beforeToolHooks = hookHarness.hooks.get("before_tool_call") || [];
	const afterToolHooks = hookHarness.hooks.get("after_tool_call") || [];
	assert.ok(beforeToolHooks.length > 0, "the real plugin entry registers before_tool_call");
	assert.ok(afterToolHooks.length > 0, "the real plugin entry registers after_tool_call");
	for (const toolName of [
		"read",
		"exec",
		"team_artifact_write",
		"team_artifact_read",
		"team_artifact_list",
		"team_send",
		"team_update_progress",
		"team_complete_task",
		"browser",
	]) {
		const event = {
			toolName,
			params: toolName === "exec"
				? { command: "pwd" }
				: toolName === "browser"
					? { action: "status" }
					: {},
		};
		for (const hook of beforeToolHooks) await hook(event, { sessionKey: "agent:main:test" });
		for (const hook of afterToolHooks) await hook({ ...event, result: { ok: true } }, { sessionKey: "agent:main:test" });
	}

	const previousHome = process.env.HOME;
	try {
		const activityHome = path.join(root, "activity-home");
		const sessionsDir = path.join(activityHome, ".openclaw", "agents", "main", "sessions");
		await fs.mkdir(sessionsDir, { recursive: true });
		process.env.HOME = activityHome;
		const sessionFile = path.join(sessionsDir, "real-openclaw-shape.jsonl");
		const startedAt = Date.now() - 1000;
		const assistantAt = new Date().toISOString();
		const resultAt = new Date(Date.now() + 1).toISOString();
		await fs.writeFile(sessionFile, [
			JSON.stringify({
				type: "message",
				id: "assistant-real-shape",
				timestamp: assistantAt,
				message: {
					role: "assistant",
					content: [
						{ type: "text", text: "正在写入协作计划。" },
						{ type: "toolCall", id: "call-real-shape", name: "team_artifact_write", arguments: {} },
					],
				},
			}),
			JSON.stringify({
				type: "message",
				id: "tool-result-real-shape",
				timestamp: resultAt,
				message: {
					role: "toolResult",
					toolCallId: "call-real-shape",
					toolName: "team_artifact_write",
					content: [{ type: "text", text: "tool failed" }],
					isError: true,
				},
			}),
		].join("\n") + "\n", "utf8");
		const completedActivity = await pluginModule.latestRuntimeSessionActivity(startedAt);
		assert.equal(completedActivity.lastActivityKind, "tool_result", "camelCase OpenClaw toolResult is observed");
		assert.equal(completedActivity.lastAssistantText, "正在写入协作计划。");
		assert.equal(completedActivity.lastToolName, "team_artifact_write");
		assert.equal(completedActivity.lastToolFailed, true);
		assert.equal(completedActivity.pendingToolName, "");

		await fs.appendFile(sessionFile, JSON.stringify({
			type: "message",
			id: "assistant-pending-real-shape",
			timestamp: new Date(Date.now() + 2).toISOString(),
			message: {
				role: "assistant",
				content: [
					{ type: "text", text: "现在派发任务。" },
					{ type: "toolCall", id: "call-pending", name: "team_send", arguments: {} },
				],
			},
		}) + "\n", "utf8");
		const pendingActivity = await pluginModule.latestRuntimeSessionActivity(startedAt);
		assert.equal(pendingActivity.lastActivityKind, "tool_call", "camelCase OpenClaw toolCall is observed");
		assert.equal(pendingActivity.pendingToolName, "team_send");
		assert.equal(pendingActivity.lastAssistantText, "现在派发任务。");
		assert.equal(pendingActivity.lastToolName, "team_artifact_write", "the last completed tool remains factual");
	} finally {
		if (previousHome === undefined) delete process.env.HOME;
		else process.env.HOME = previousHome;
	}

	const liveRuntime = pluginModule.createRuntime({ config: {}, logger: { warn() {} } });
	const liveNarratives = [];
	const liveEmitter = async (text, source, media, meta) => {
		liveNarratives.push({ text, source, media, meta });
		return true;
	};
	const liveEnvelope = {
		teamId: "75",
		memberId: "developer",
		role: "developer",
		taskId: "team-75-task-live",
		rootTaskId: "team-75-task-live",
		messageId: "msg-live",
		assignmentId: "dev-live",
	};
	await liveRuntime.withActiveEnvelope(
		liveEnvelope,
		async () => liveRuntime.withNarrativeProjection(liveEnvelope, liveEmitter, async () => {
			liveRuntime.observeAssistantSessionMessage({
				message: {
					id: "assistant-live-1",
					role: "assistant",
					timestamp: new Date().toISOString(),
					content: [
						{ type: "text", text: "正在读取产物并开始检查。" },
						{ type: "tool_use", name: "team_artifact_read" },
					],
				},
			}, { sessionKey: "redis-team:75:developer" });
			liveRuntime.observeAssistantSessionMessage({
				message: { id: "assistant-live-2", role: "assistant", content: [{ type: "text", text: "NO_REPLY" }] },
			}, { sessionKey: "redis-team:75:developer" });
			await liveRuntime.flushAssistantSessionNarratives();
		}),
		{ teamId: "75", memberId: "developer", role: "developer", sharedDir: shared },
	);
	assert.equal(liveNarratives.length, 1, "live projection emits visible assistant text but not NO_REPLY");
	assert.equal(liveNarratives[0].text, "正在读取产物并开始检查。");
	assert.equal(liveNarratives[0].source, "before_message_write");
	assert.equal(liveNarratives[0].meta.sourceRecordId, "assistant-live-1");
	const concurrentNarratives = { first: [], second: [] };
	const concurrentEnvelope = (suffix) => ({
		...liveEnvelope,
		taskId: `team-75-task-${suffix}`,
		rootTaskId: `team-75-task-${suffix}`,
		messageId: `msg-${suffix}`,
		assignmentId: `dev-${suffix}`,
	});
	await Promise.all([
		liveRuntime.withNarrativeProjection(
			concurrentEnvelope("first"),
			async (text) => { concurrentNarratives.first.push(text); return true; },
			async () => {
				await Promise.resolve();
				liveRuntime.observeAssistantSessionMessage({
					message: { id: "assistant-first", role: "assistant", content: [{ type: "text", text: "first task update" }] },
				});
			},
		),
		liveRuntime.withNarrativeProjection(
			concurrentEnvelope("second"),
			async (text) => { concurrentNarratives.second.push(text); return true; },
			async () => {
				liveRuntime.observeAssistantSessionMessage({
					message: { id: "assistant-second", role: "assistant", content: [{ type: "text", text: "second task update" }] },
				});
				await Promise.resolve();
			},
		),
	]);
	assert.deepEqual(concurrentNarratives.first, ["first task update"], "concurrent tasks keep the first narrative isolated");
	assert.deepEqual(concurrentNarratives.second, ["second task update"], "concurrent tasks keep the second narrative isolated");
	const crossTeamNarratives = { first: [], second: [] };
	let releaseCrossTeamFirst;
	let releaseCrossTeamSecond;
	const crossTeamFirstEnvelope = { ...concurrentEnvelope("cross-first"), teamId: "75" };
	const crossTeamSecondEnvelope = { ...concurrentEnvelope("cross-second"), teamId: "76" };
	const crossTeamFirst = liveRuntime.withNarrativeProjection(
		crossTeamFirstEnvelope,
		async (text) => { crossTeamNarratives.first.push(text); return true; },
		async () => new Promise((resolve) => { releaseCrossTeamFirst = resolve; }),
	);
	const crossTeamSecond = liveRuntime.withNarrativeProjection(
		crossTeamSecondEnvelope,
		async (text) => { crossTeamNarratives.second.push(text); return true; },
		async () => new Promise((resolve) => { releaseCrossTeamSecond = resolve; }),
	);
	await Promise.resolve();
	liveRuntime.observeAssistantSessionMessage({
		sessionKey: "agent:main:redis-team:group:76",
		message: { id: "assistant-team-76", role: "assistant", content: [{ type: "text", text: "team 76 update" }] },
	});
	releaseCrossTeamFirst();
	releaseCrossTeamSecond();
	await Promise.all([crossTeamFirst, crossTeamSecond]);
	assert.deepEqual(crossTeamNarratives.first, [], "a detached hook never crosses into another Team");
	assert.deepEqual(crossTeamNarratives.second, ["team 76 update"], "a detached hook binds by exact Team session");
	const detachedNarratives = [];
	let releaseDetachedProjection;
	const detachedProjection = liveRuntime.withNarrativeProjection(
		concurrentEnvelope("detached"),
		async (text) => { detachedNarratives.push(text); return true; },
		async () => new Promise((resolve) => { releaseDetachedProjection = resolve; }),
	);
	await Promise.resolve();
	liveRuntime.observeAssistantSessionMessage({
		message: { id: "assistant-detached", role: "assistant", content: [{ type: "text", text: "detached hook update" }] },
	}, { sessionKey: "redis-team:75:developer:detached" });
	releaseDetachedProjection();
	await detachedProjection;
	assert.deepEqual(
		detachedNarratives,
		["detached hook update"],
		"a detached OpenClaw transcript hook binds to the only active projection",
	);
	const ambiguousNarratives = { first: [], second: [] };
	let releaseAmbiguousFirst;
	let releaseAmbiguousSecond;
	const ambiguousFirst = liveRuntime.withNarrativeProjection(
		concurrentEnvelope("ambiguous-first"),
		async (text) => { ambiguousNarratives.first.push(text); return true; },
		async () => new Promise((resolve) => { releaseAmbiguousFirst = resolve; }),
	);
	const ambiguousSecond = liveRuntime.withNarrativeProjection(
		concurrentEnvelope("ambiguous-second"),
		async (text) => { ambiguousNarratives.second.push(text); return true; },
		async () => new Promise((resolve) => { releaseAmbiguousSecond = resolve; }),
	);
	await Promise.resolve();
	liveRuntime.observeAssistantSessionMessage({
		message: { id: "assistant-ambiguous", role: "assistant", content: [{ type: "text", text: "must not cross-deliver" }] },
	}, { sessionKey: "redis-team:75:unknown-session" });
	releaseAmbiguousFirst();
	releaseAmbiguousSecond();
	await Promise.all([ambiguousFirst, ambiguousSecond]);
	assert.deepEqual(ambiguousNarratives, { first: [], second: [] }, "an ambiguous detached hook stays fail-soft");
	assert.deepEqual(
		pluginModule.completionProposalProvenance({ automaticTurnResult: true }),
		{ completionSource: "team_complete_task", explicitCompletion: true },
		"completion proposal provenance is reserved for the explicit completion tool",
	);
	assert.deepEqual(
		pluginModule.completionProposalProvenance({}),
		{ completionSource: "team_complete_task", explicitCompletion: true },
		"the explicit completion tool keeps its original provenance",
	);
  await fs.writeFile(path.join(shared, "team.json"), JSON.stringify({
    version: 1,
    teamId: "75",
    rosterHash: "sha256:team-75-roster-v1",
    leaderMemberId: "leader",
    communicationMode: "leader_mediated",
    members: [
      { memberId: "leader", displayName: "Leader", role: "leader", effectiveRole: "leader", isLeader: true },
      { memberId: "developer", displayName: "Developer", role: "developer", effectiveRole: "developer" },
    ],
  }), "utf8");
  await fs.writeFile(
    path.join(shared, "team-introduction.md"),
    "# Team 启动介绍\n\nLeader coordinates the Developer.\n",
    "utf8",
  );
	const fastTeamSend = await pluginModule.normalizeTeamSendParams(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		{ to: "developer", text: "Implement the page." },
		null,
	);
	assert.deepEqual(fastTeamSend.params, { to: "developer", text: "Implement the page." });
	const aliasedTeamSend = await pluginModule.normalizeTeamSendParams(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		{ recipient: "developer", message: "Implement the page." },
		null,
	);
	assert.equal(aliasedTeamSend.params.to, "developer");
	assert.equal(aliasedTeamSend.params.text, "Implement the page.");
	const replyTeamSend = await pluginModule.normalizeTeamSendParams(
		{ teamId: "75", memberId: "developer", sharedDir: shared },
		{ text: "The implementation is ready." },
		{ from: "leader", assignmentId: "dev-01" },
	);
	assert.equal(replyTeamSend.params.to, "leader", "an authenticated assignment sender is a safe reply target");
	const ambiguousTeamSend = await pluginModule.normalizeTeamSendParams(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		{ text: "Start the next assignment." },
		{ from: "clawmanager", rootTaskId: "team-75-task-149" },
	);
	assert.equal(ambiguousTeamSend.error.code, "ambiguous_team_target");
	assert.equal(ambiguousTeamSend.error.sent, false);
	assert.deepEqual(ambiguousTeamSend.error.candidates, ["developer"]);
	const conflictingTeamSend = await pluginModule.normalizeTeamSendParams(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		{ to: "developer", recipient: "leader", text: "Do not dispatch this." },
		null,
	);
	assert.equal(conflictingTeamSend.error.code, "conflicting_team_target");
  const leaderContextEnvelope = pluginModule.normalizeEnvelope({
    teamId: "75",
    from: "clawmanager",
    to: "leader",
    taskId: "team-75-task-149",
    rootTaskId: "team-75-task-149",
    messageId: "root-149",
    turnOutcomePolicy: { actionExpected: true, immediateRecoveryAllowed: false, reason: "test" },
    metadata: {},
  });
  assert.deepEqual(leaderContextEnvelope.turnOutcomePolicy, {
    actionExpected: true,
    immediateRecoveryAllowed: false,
    reason: "test",
  });
  const fullLeaderContext = await pluginModule.appendLeaderTeamContext(
    "Please inspect the team.",
    { teamId: "75", memberId: "leader", role: "leader", sharedDir: shared },
    leaderContextEnvelope,
  );
  assert.match(fullLeaderContext, /Managed Team operating introduction/);
  assert.match(fullLeaderContext, /Leader coordinates the Developer/);
  assert.match(fullLeaderContext, /sha256:team-75-roster-v1/);
  const reviewerBrowserWithoutDeclaredTarget = pluginModule.reviewerBrowserToolDecision(
    { role: "reviewer", taskId: "team-75-task-149", text: "Review the local HTML artifact." },
    { toolName: "browser", params: { action: "start" } },
    {},
    1000,
  );
  assert.notEqual(reviewerBrowserWithoutDeclaredTarget.block, true);
  assert.equal(
    pluginModule.verificationTargetUrl({
      role: "reviewer",
      text: "Read https://example.com as source material, but no verification target was declared.",
    }),
    "",
    "ordinary URLs in assignment prose must not trigger Browser verification",
  );
  const browserState = {};
  const browserEnvelope = pluginModule.normalizeEnvelope({
    role: "reviewer",
    taskId: "team-75-task-149",
    reviewedAssignmentId: "dev-01",
    reviewedRevision: 2,
    verificationUrl: "https://example.com/review",
  });
  assert.equal(browserEnvelope.reviewedAssignmentId, "dev-01");
  assert.equal(browserEnvelope.reviewedRevision, 2);
  assert.equal(browserEnvelope.verificationUrl, "https://example.com/review");
	for (const action of ["status", "start"]) {
		const preparation = pluginModule.reviewerBrowserToolDecision(
			browserEnvelope,
			{ toolName: "browser", params: { action } },
			browserState,
			1000,
		);
		assert.notEqual(preparation.block, true);
		assert.equal(browserState.startedAt, undefined, `${action} must not start the review budget`);
	}
	pluginModule.reviewerBrowserToolDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: "https://example.com/review" } },
		browserState,
    1001,
  );
  assert.equal(browserState.startedAt, undefined, "the budget starts only after a successful open");
  assert.deepEqual(
    pluginModule.lateNarrativeProjectionMeta(true),
    { lateProjection: true, suppressedAfterTerminal: true, terminalDelivery: false },
    "ordinary session reconciliation prose must be hidden after terminal delivery",
  );
  assert.deepEqual(
    pluginModule.lateNarrativeProjectionMeta(false),
    { lateProjection: true, suppressedAfterTerminal: false, terminalDelivery: false },
    "pre-terminal recovery may retain real narrative without affecting workflow state",
  );
  const visibleLeaderReply = "Development dispatched; waiting for the worker result.";
  assert.equal(pluginModule.normalizeAssistantSessionText("NO_REPLY"), "");
  assert.equal(pluginModule.normalizeAssistantSessionText("Redis Team task completed"), "");
  assert.equal(
    pluginModule.normalizeAssistantSessionText("The phrase Redis Team task completed is a Runtime control placeholder."),
    "The phrase Redis Team task completed is a Runtime control placeholder.",
    "only the standalone Runtime control reply is hidden",
  );
  assert.equal(
    pluginModule.normalizeAssistantSessionText(`${visibleLeaderReply}\n\nNO_REPLY`),
    visibleLeaderReply,
    "the raw session copy must hash like OpenClaw's normalized callback",
  );
  assert.equal(
    pluginModule.normalizeAssistantSessionText("OpenClaw uses NO_REPLY as its silent token."),
    "OpenClaw uses NO_REPLY as its silent token.",
    "ordinary prose discussing the token must remain visible",
  );
  const sessionNarratives = [
    { text: "internal setup" },
    { text: visibleLeaderReply },
  ];
  assert.deepEqual(
    pluginModule.assistantSessionNarrativesForProjection(sessionNarratives, true, false),
    [],
    "a successful delivery callback owns visible projection",
  );
  assert.deepEqual(
    pluginModule.assistantSessionNarrativesForProjection(sessionNarratives, false, false),
    [sessionNarratives[1]],
    "callback-free compatibility recovery must project only the latest narrative",
  );
  assert.deepEqual(
    pluginModule.assistantSessionNarrativesForProjection(sessionNarratives, false, true),
    [],
    "terminal completion owns final delivery and old process prose must not replay afterward",
  );
  const guardEnvelope = {
    rootTaskId: "team-75-task-150",
    assignmentId: "review-150",
  };
  const openGuardKey = pluginModule.reviewerBrowserGuardKey(
    guardEnvelope,
    { toolName: "browser", params: { action: "open", url: "http://managed.example/preview" } },
    { runId: "run-150" },
  );
  const snapshotGuardKey = pluginModule.reviewerBrowserGuardKey(
    guardEnvelope,
    { toolName: "browser", params: { action: "snapshot", targetId: "target-150" } },
    { runId: "run-150" },
  );
  assert.equal(
    openGuardKey,
    snapshotGuardKey,
    "Browser guard state must survive actions that omit the navigation URL",
  );
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: "https://example.com/review" }, result: { status: "ok" } },
		browserState,
		1002,
	);
	assert.equal(browserState.startedAt, 1002);
	const managedBrowserState = {};
	const managedPreviewUrl = "http://clawmanager-egress-proxy.clawmanager-hxc-peer-system.svc.cluster.local:3128/v2/interactive/75/_/signature/index.html";
	pluginModule.reviewerBrowserToolDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: managedPreviewUrl } },
		managedBrowserState,
		1010,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: managedPreviewUrl }, result: { status: "ok" } },
		managedBrowserState,
		1011,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "snapshot" }, result: { status: "ok" } },
		managedBrowserState,
		1012,
	);
	assert.deepEqual(
		pluginModule.browserVerificationForCompletion(browserEnvelope, managedBrowserState),
		{
			verificationMode: "managed_browser",
			browserVerification: {
				status: "verified",
				source: "runtime_tool_events",
				managedPreview: true,
				opened: true,
				inspected: true,
				targetHash: undefined,
				evidenceIncomplete: undefined,
			},
		},
		"only successful managed Browser tool events may produce a managed-browser verification fact",
	);
	const navigateBrowserState = {};
	const isolatedPreviewUrl = "http://p-abcdefghijklmnop.clawmanager-team-preview.invalid/v2/interactive/75/_/signature/index.html";
	const navigateDecision = pluginModule.reviewerBrowserToolDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "navigate", url: managedPreviewUrl } },
		navigateBrowserState,
		1020,
	);
	assert.notEqual(navigateDecision.block, true, "normal Browser navigate must establish a target instead of being treated as inspect-before-open");
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "navigate", url: managedPreviewUrl }, result: { url: isolatedPreviewUrl, targetId: "target-nav" } },
		navigateBrowserState,
		1021,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "snapshot" }, result: { status: "ok" } },
		navigateBrowserState,
		1022,
	);
	assert.equal(navigateBrowserState.targetUrl, isolatedPreviewUrl, "redirected Preview origin is retained as the verified target");
	assert.equal(navigateBrowserState.managedPreviewOpened, true);
	assert.equal(navigateBrowserState.managedPreviewInspected, true);
	assert.equal(
		pluginModule.browserToolResultUrl({ result: { details: { url: isolatedPreviewUrl } } }),
		isolatedPreviewUrl,
		"persisted Browser tool results recover the final URL even when after_tool_call delivery is detached",
	);
	const activeBrowserEnvelope = {
		...browserEnvelope,
		teamId: "75",
		memberId: "reviewer",
		role: "reviewer",
		rootTaskId: "team-75-task-149",
		messageId: "reviewer-browser-turn",
		assignmentId: "review-149",
	};
	const activeBrowserResult = await liveRuntime.withActiveEnvelope(
		activeBrowserEnvelope,
		async () => {
			const state = {};
			liveRuntime.beforeBrowserToolCall(
				{ toolName: "browser", params: { action: "open", url: managedPreviewUrl } },
				state,
				1020,
			);
			await liveRuntime.afterBrowserToolCall(
				{ toolName: "browser", params: { action: "open", url: managedPreviewUrl }, result: { status: "ok" } },
				state,
				1021,
			);
			await liveRuntime.afterBrowserToolCall(
				{ toolName: "browser", params: { action: "evaluate" }, result: { status: "ok" } },
				state,
				1022,
			);
		},
		{ teamId: "75", memberId: "reviewer", role: "reviewer", sharedDir: shared },
	);
	assert.equal(activeBrowserResult.browserVerification.managedPreviewOpened, true);
	assert.equal(activeBrowserResult.browserVerification.managedPreviewInspected, true);
	assert.equal(activeBrowserResult.browserVerification.lastSuccessfulAction, "evaluate");
	let detachedBrowserState;
	await liveRuntime.withActiveEnvelope(
		activeBrowserEnvelope,
		async () => {
			detachedBrowserState = {};
			liveRuntime.beforeBrowserToolCall(
				{ toolName: "browser", params: { action: "open", url: managedPreviewUrl } },
				detachedBrowserState,
				1030,
			);
		},
		{ teamId: "75", memberId: "reviewer", role: "reviewer", sharedDir: shared },
	);
	await liveRuntime.afterBrowserToolCall(
		{ toolName: "browser", params: { action: "open", url: managedPreviewUrl }, result: { status: "ok" } },
		detachedBrowserState,
		1031,
	);
	await liveRuntime.afterBrowserToolCall(
		{ toolName: "browser", params: { action: "snapshot" }, result: { status: "ok" } },
		detachedBrowserState,
		1032,
	);
	assert.equal(detachedBrowserState.verification.managedPreviewOpened, true);
	assert.equal(detachedBrowserState.verification.managedPreviewInspected, true, "detached after_tool_call keeps the immutable assignment binding");
	assert.equal(
		pluginModule.browserVerificationForCompletion(browserEnvelope, {}).verificationMode,
		"static_only",
		"a Reviewer may still complete after static review without claiming Browser verification",
	);
	assert.equal(
		pluginModule.browserVerificationForCompletion(browserEnvelope, { evidenceIncomplete: true }).verificationMode,
		"unknown",
		"lost Browser evidence must not be misreported as a factual static-only review",
	);
  for (let index = 0; index < 10; index += 1) {
    const decision = pluginModule.reviewerBrowserToolDecision(
      browserEnvelope,
			{ toolName: "browser", params: { action: "snapshot" } },
      browserState,
			1003 + index,
    );
    assert.notEqual(decision.block, true);
		pluginModule.reviewerBrowserToolResultDecision(
			browserEnvelope,
			{ toolName: "browser", params: { action: "snapshot" }, result: { status: "ok" } },
			browserState,
			1003 + index,
		);
  }
  const exhaustedBrowser = pluginModule.reviewerBrowserToolDecision(
    browserEnvelope,
    { toolName: "browser", params: { action: "snapshot" } },
    browserState,
		1008,
  );
  assert.equal(exhaustedBrowser.block, true);
	const elasticState = {};
	pluginModule.reviewerBrowserToolDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: "https://example.com/review" } },
		elasticState,
		3000,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "open", url: "https://example.com/review" }, result: { status: "ok" } },
		elasticState,
		3001,
	);
	pluginModule.reviewerBrowserToolDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "evaluate" } },
		elasticState,
		3002,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "evaluate" }, error: "SyntaxError: unexpected token" },
		elasticState,
		3003,
	);
	assert.equal(elasticState.calls, 0, "a recoverable Browser error must not consume successful evidence budget");
	assert.notEqual(
		pluginModule.reviewerBrowserToolDecision(
			browserEnvelope,
			{ toolName: "browser", params: { action: "evaluate" } },
			elasticState,
			3004,
		).block,
		true,
		"one corrected Browser call remains available",
	);
	pluginModule.reviewerBrowserToolResultDecision(
		browserEnvelope,
		{ toolName: "browser", params: { action: "evaluate" }, result: { status: "ok" } },
		elasticState,
		3005,
	);
	assert.equal(elasticState.calls, 1);
  assert.equal(pluginModule.browserToolCallFailed({
    result: {
      content: [{ type: "text", text: JSON.stringify({ status: "error", tool: "browser", error: "navigation blocked" }) }],
    },
  }), true);
  assert.equal(pluginModule.browserToolCallFailed({
    result: { content: [{ type: "text", text: JSON.stringify({ status: "ok", title: "Rendered" }) }] },
  }), false);
	for (const command of [
		"python3 -m http.server 8765",
		"pkill -f remote-debugging-port=9222",
		"chromium --headless --remote-debugging-port=9222",
		"fuser 8765/tcp -k",
	]) {
		assert.equal(
			pluginModule.teamProcessToolDecision(null, { toolName: "exec", params: { command } }).block,
			true,
			`dangerous over-testing command must be blocked: ${command}`,
		);
	}
	assert.notEqual(
		pluginModule.teamProcessToolDecision(
			{ instanceMode: "pro", role: "reviewer" },
			{ toolName: "exec", params: { command: "chromium --headless --remote-debugging-port=9222" } },
		).block,
		true,
		"an isolated Pro instance may use an explicitly assigned local Browser harness",
	);
	assert.equal(
		pluginModule.teamProcessToolDecision(
			{ instanceMode: "pro", role: "reviewer" },
			{ toolName: "exec", params: { command: "pkill chromium" } },
		).block,
		true,
		"broad process termination remains unsafe even in an isolated instance",
	);
	for (const command of ["npm test", "npm run dev", "ps -ef", "kill 12345", "python3 scripts/check.py"]) {
		assert.notEqual(
			pluginModule.teamProcessToolDecision(null, { toolName: "exec", params: { command } }).block,
			true,
			`normal project verification must remain available: ${command}`,
		);
	}
	for (const command of [
		"chromium --headless file:///team/results/index.html",
		"npx playwright test verify.spec.js",
		"node -e \"require('puppeteer').launch()\"",
		"npm install puppeteer",
		"pip install selenium",
	]) {
		assert.notEqual(
			pluginModule.teamProcessToolDecision({ role: "reviewer" }, { toolName: "exec", params: { command } }).block,
			true,
			`an explicitly assigned validator must not be role-blocked from required validation tooling: ${command}`,
		);
	}
	for (const command of ["npm test", "npm run build", "python3 scripts/check.py", "rg puppeteer package.json"]) {
		assert.notEqual(
			pluginModule.teamProcessToolDecision({ role: "reviewer" }, { toolName: "exec", params: { command } }).block,
			true,
			`Reviewer source review and existing project checks must remain available: ${command}`,
		);
	}
	assert.notEqual(
		pluginModule.teamProcessToolDecision({ role: "developer" }, {
			toolName: "exec",
			params: { command: "chromium --headless file:///workspace/index.html" },
		}).block,
		true,
		"ordinary Developers must not inherit the Reviewer-only Browser bypass guard",
	);
	assert.equal(pluginModule.assignmentHasIndependentReview({ role: "developer", reviewRequired: true }), true);
	assert.equal(pluginModule.assignmentHasIndependentReview({ role: "reviewer", reviewRequired: true }), true);
	assert.equal(pluginModule.assignmentHasIndependentReview({ role: "developer", validationAssignment: true }), false);
	assert.notEqual(
		pluginModule.teamProcessToolDecision(
			{ role: "developer", reviewRequired: true },
			{ toolName: "exec", params: { command: "npm install puppeteer" } },
		).block,
		true,
		"validation ownership is soft guidance and must not hard-block an implementation tool call",
	);
	assert.notEqual(
		pluginModule.teamProcessToolDecision(
			{ role: "developer", validationAssignment: true, validationTargetAssignmentId: "dev-01" },
			{ toolName: "exec", params: { command: "npm install puppeteer" } },
		).block,
		true,
		"an explicit member-owned validation assignment must remain available regardless of role",
	);
	assert.deepEqual(
		pluginModule.mergeBrowserVerificationState(
			{ managedPreviewOpened: true, managedPreviewInspected: true, lastSuccessfulAction: "snapshot" },
			{ managedPreviewOpened: false, managedPreviewInspected: false, lastFailureAction: "evaluate" },
		),
		{
			managedPreviewOpened: true,
			managedPreviewInspected: true,
			lastSuccessfulAction: "snapshot",
			lastFailureAction: "evaluate",
		},
		"a later Browser failure must not erase earlier successful evidence",
	);
  assert.deepEqual(
    pluginModule.normalizePhaseDispositions([
      { phaseId: "phase-2", decision: "skipped", reason: "Phase 1 fully satisfied the goal." },
      { phaseId: "phase-2", decision: "cancelled", reason: "duplicate must be ignored" },
      { phaseId: "phase-3", decision: "completed", reason: "invalid disposition" },
      { phaseId: "phase-4", decision: "superseded", reason: "" },
    ]),
    [{ phaseId: "phase-2", decision: "skipped", reason: "Phase 1 fully satisfied the goal." }],
  );
  for (let index = 0; index < 8; index += 1) {
    const developerBrowser = pluginModule.reviewerBrowserToolDecision(
      { role: "developer", taskId: "team-75-task-149" },
      { toolName: "browser", params: { action: "snapshot" } },
      {},
      2000 + index,
    );
    assert.notEqual(developerBrowser.block, true, "Developer Browser must not inherit the Reviewer budget");
  }
  const genericValidatorState = {};
	const genericValidatorEnvelope = {
		role: "domain-specialist",
		validationAssignment: true,
		validationTargetAssignmentId: "dev-01",
		taskId: "team-75-task-149",
	};
	pluginModule.reviewerBrowserToolDecision(
		genericValidatorEnvelope,
		{ toolName: "browser", params: { action: "open", url: "https://example.com/review" } },
		genericValidatorState,
		3000,
	);
	pluginModule.reviewerBrowserToolResultDecision(
		genericValidatorEnvelope,
		{ toolName: "browser", params: { action: "open" }, result: { status: "ok" } },
		genericValidatorState,
		3001,
	);
  for (let index = 0; index < 10; index += 1) {
    const genericValidatorBrowser = pluginModule.reviewerBrowserToolDecision(
			genericValidatorEnvelope,
      { toolName: "browser", params: { action: "snapshot" } },
      genericValidatorState,
			3002 + index,
    );
    assert.notEqual(genericValidatorBrowser.block, true);
		pluginModule.reviewerBrowserToolResultDecision(
			genericValidatorEnvelope,
			{ toolName: "browser", params: { action: "snapshot" }, result: { status: "ok" } },
			genericValidatorState,
			3002 + index,
		);
  }
  assert.equal(
    pluginModule.reviewerBrowserToolDecision(
		genericValidatorEnvelope,
      { toolName: "browser", params: { action: "snapshot" } },
      genericValidatorState,
		3007,
    ).block,
    true,
    "any assigned validator gets the brief Browser budget without reducing ordinary Worker Browser access",
  );
  assert.equal(pluginModule.rootWorkflowStateIsTerminal({ terminal: true }), true);
  assert.equal(pluginModule.rootWorkflowStateIsTerminal({ status: "succeeded" }), true);
  assert.equal(pluginModule.rootWorkflowStateIsTerminal({ status: "running" }), false);
  const compactLeaderContext = await pluginModule.appendLeaderTeamContext(
    "Please inspect the team again.",
    { teamId: "75", memberId: "leader", role: "leader", sharedDir: shared },
    { ...leaderContextEnvelope, taskId: "team-75-task-150", rootTaskId: "team-75-task-150", messageId: "root-150" },
  );
  assert.match(compactLeaderContext, /Managed Team roster snapshot/);
  assert.doesNotMatch(compactLeaderContext, /Managed Team operating introduction/);
  const workerContext = await pluginModule.appendLeaderTeamContext(
    "Implement it.",
    { teamId: "75", memberId: "developer", role: "developer", sharedDir: shared },
    { ...leaderContextEnvelope, to: "developer", taskId: "team-75-task-151", rootTaskId: "team-75-task-151" },
  );
  assert.equal(workerContext, "Implement it.", "worker prompts must not receive Leader roster preloading");
  const turnFinished = pluginModule.turnFinishedWithoutCompletionEvent(
    { metadata: { completionRecoveryAttempt: 1 } },
    { assistantNarratives: [{ text: "Final answer without tool receipt." }] },
  );
  assert.equal(turnFinished.eventKind, "turn_finished_without_completion");
  assert.equal(turnFinished.activeTurnFinished, true);
  assert.equal(turnFinished.hadAssistantNarrative, true);
  assert.equal(turnFinished.hadOutboundAssignment, false);
  assert.equal(turnFinished.completionRecoveryAttempt, 1);
  assert.equal(turnFinished.visibleToChat, false);
  assert.equal(turnFinished.chatPolicy, "hidden");
  assert.equal(turnFinished.status, "running");
  assert.equal(turnFinished.stateEffect, "none");
  assert.equal(turnFinished.rootTaskTerminal, false);
  const failedHandoffTurn = pluginModule.turnFinishedWithoutCompletionEvent(
    { metadata: { completionRecoveryAttempt: 0 } },
    {
      fallbackText: "The team_send failed because the to field was missing. Let me resend with to: leader.",
      assistantNarratives: [{ text: "The team_send failed because the to field was missing." }],
      lastToolOutcome: { failed: true, toolName: "team_send", toolCallId: "call-team-send" },
      browserVerification: {
        verificationMode: "managed_browser",
        browserVerification: { status: "verified", opened: true, inspected: true },
      },
    },
  );
  assert.equal(failedHandoffTurn.lastToolFailed, true);
  assert.equal(failedHandoffTurn.completionContinuationRequired, true);
  assert.equal(failedHandoffTurn.lastToolName, "team_send");
  assert.equal(failedHandoffTurn.verificationMode, "managed_browser");
  assert.match(failedHandoffTurn.resultMarkdown, /Let me resend/);
  const toolCalls = new Map();
  assert.equal(pluginModule.sessionToolOutcome({
    message: { role: "assistant", content: [{ type: "tool_use", id: "call-1", name: "team_send" }] },
  }, toolCalls), null);
  assert.deepEqual(pluginModule.sessionToolOutcome({
    id: "tool-result-1",
    message: {
      role: "tool",
      content: [{ type: "tool_result", tool_use_id: "call-1", is_error: true, content: "missing required field: to" }],
    },
  }, toolCalls), {
    failed: true,
    retryable: true,
    succeeded: false,
    toolName: "team_send",
    toolCallId: "call-1",
    sourceRecordId: "tool-result-1",
  });
  const retryableToolOutcome = pluginModule.sessionToolOutcome({
    id: "tool-result-2",
    message: {
      role: "tool",
      tool_call_id: "call-2",
      name: "team_send",
      content: [{ type: "text", text: JSON.stringify({ ok: false, retryable: true, code: "ambiguous_team_target", candidates: ["developer", "reviewer"] }) }],
    },
  }, new Map());
  assert.equal(retryableToolOutcome.failed, true, "structured ok=false is a failed tool call even when OpenClaw did not set isError");
  assert.equal(retryableToolOutcome.retryable, true);
  assert.equal(retryableToolOutcome.code, "ambiguous_team_target");
  assert.deepEqual(retryableToolOutcome.candidates, ["developer", "reviewer"]);
  const correctedToolSession = path.join(root, "corrected-tool-session.jsonl");
  const correctedAt = new Date().toISOString();
  await fs.writeFile(correctedToolSession, [
    JSON.stringify({
      timestamp: correctedAt,
      message: { role: "assistant", content: [{ type: "tool_use", id: "send-failed", name: "team_send" }] },
    }),
    JSON.stringify({
      timestamp: correctedAt,
      message: { role: "tool", content: [{ type: "tool_result", tool_use_id: "send-failed", text: JSON.stringify({ ok: false, retryable: true }) }] },
    }),
    JSON.stringify({
      timestamp: correctedAt,
      message: { role: "assistant", content: [{ type: "tool_use", id: "send-corrected", name: "team_send" }] },
    }),
    JSON.stringify({
      timestamp: correctedAt,
      message: { role: "tool", content: [{ type: "tool_result", tool_use_id: "send-corrected", text: JSON.stringify({ ok: true, sent: true }) }] },
    }),
  ].join("\n") + "\n", "utf8");
  const correctedEvidence = await pluginModule.readTurnToolEvidenceFromDispatch({ storePath: correctedToolSession });
  assert.equal(correctedEvidence.retryableTeamToolGap, null, "a later successful call in the same Team tool family resolves the gap");
  assert.equal(correctedEvidence.lastToolOutcome.succeeded, true);
  const retryableObservation = pluginModule.observeTeamTurnOutcome({
    envelope: {
      requiresCompletion: false,
      intent: "member_result_confirmed",
      turnOutcomePolicy: { actionExpected: true, immediateRecoveryAllowed: true },
    },
    activeResult: {},
    durableFacts: { available: true, completionProposed: false },
    toolEvidence: { source: "dispatch_session", retryableTeamToolGap: retryableToolOutcome },
    contextOnly: true,
  });
  assert.equal(retryableObservation.outcome, "retryable_tool_gap");
  assert.equal(retryableObservation.immediateRecoveryEligible, true);
  const conflictingObservation = pluginModule.observeTeamTurnOutcome({
    envelope: { requiresCompletion: false, intent: "member_result_confirmed" },
    activeResult: { completionPending: true },
    durableFacts: { available: true, completionProposed: true },
    toolEvidence: { source: "dispatch_session", retryableTeamToolGap: retryableToolOutcome },
    contextOnly: true,
  });
  assert.equal(conflictingObservation.outcome, "runtime_observation_unknown", "conflicting facts must degrade instead of driving recovery");
  assert.equal(conflictingObservation.immediateRecoveryEligible, false);
  const ordinaryContextObservation = pluginModule.observeTeamTurnOutcome({
    envelope: { requiresCompletion: false, intent: "context" },
    activeResult: {},
    durableFacts: { available: true, completionProposed: false },
    toolEvidence: { source: "dispatch_session", retryableTeamToolGap: null },
    contextOnly: true,
  });
  assert.equal(ordinaryContextObservation.outcome, "ordinary_open_turn");
  assert.equal(ordinaryContextObservation.immediateRecoveryEligible, false, "ordinary context must never create a reminder loop");
  const handoffObservation = pluginModule.observeTeamTurnOutcome({
    envelope: { requiresCompletion: false, intent: "member_result_confirmed" },
    activeResult: { outbound: { message: { to: "reviewer" } } },
    durableFacts: { available: true, completionProposed: false },
    toolEvidence: { source: "dispatch_session", retryableTeamToolGap: null },
    contextOnly: true,
  });
  assert.equal(handoffObservation.outcome, "legitimate_wait");
  assert.equal(handoffObservation.immediateRecoveryEligible, false, "a successful handoff must not be nudged again");
  const incompleteAttempt = pluginModule.assignmentAttemptFailedEvent({ taskId: "team-75-task-150" });
  assert.equal(incompleteAttempt.eventKind, "assignment_attempt_failed");
  assert.equal(incompleteAttempt.stateEffect, "none");
  assert.equal(incompleteAttempt.retryable, true);
  assert.equal(incompleteAttempt.rootTaskTerminal, false);
  assert.equal(pluginModule.isIncompleteTurnDelivery({
    isError: true,
    text: "Agent couldn't generate a response. Please try again.",
  }), true);
  assert.equal(pluginModule.isIncompleteTurnDelivery({ isError: true, text: "Unauthorized" }), false);
  assert.deepEqual(
    pluginModule.mergeActiveTurnFacts(
      { outbound: null, completionPending: false, artifactRefs: ["/team/a.md"] },
      { outbound: { message: { to: "developer" } }, completionProposed: true, artifactRefs: ["/team/a.md", "/team/b.md"] },
    ),
    {
      outbound: { message: { to: "developer" } },
      completionPending: true,
      artifactRefs: ["/team/a.md", "/team/b.md"],
    },
    "dispatch completion must recover facts emitted by a separate plugin instance",
  );
  const leaderDispatchRouting = await pluginModule.activeMemberRouting(
    { teamId: "75", memberId: "leader", role: "leader", sharedDir: shared },
    { message: { to: "developer", text: "Implement the page." } },
  );
  assert.equal(leaderDispatchRouting.leaderCoordination, true);
  assert.equal(leaderDispatchRouting.workerDelivery, false);
  const workerDeliveryRouting = await pluginModule.activeMemberRouting(
    { teamId: "75", memberId: "developer", role: "developer", sharedDir: shared },
    { message: { to: "leader", text: "Delivery complete." } },
  );
  assert.equal(workerDeliveryRouting.leaderCoordination, false);
  assert.equal(workerDeliveryRouting.workerDelivery, true);
  const workerQuestionRouting = await pluginModule.activeMemberRouting(
    { teamId: "75", memberId: "developer", role: "developer", sharedDir: shared },
    { message: { to: "leader", text: "Should I change the current color palette?" } },
  );
  assert.equal(workerQuestionRouting.workerToLeader, true);
  assert.equal(workerQuestionRouting.workerDelivery, false, "a Worker question must not close its assignment");
  const workerArtifactDeliveryRouting = await pluginModule.activeMemberRouting(
    { teamId: "75", memberId: "developer", role: "developer", sharedDir: shared },
    { message: { to: "leader", text: "产物：/team/artifacts/team-75-task-150/members/developer/dev-01/kanban.html" } },
  );
  assert.equal(workerArtifactDeliveryRouting.workerDelivery, true);
  const failedLeaderDispatchRouting = await pluginModule.activeMemberRouting(
    { teamId: "75", memberId: "leader", role: "leader", sharedDir: shared },
    { failed: true, message: { to: "missing-worker", text: "Implement the page." } },
  );
  assert.equal(
    failedLeaderDispatchRouting.leaderCoordination,
    true,
    "a failed or unknown Leader dispatch must never be mistaken for root completion",
  );
  const controlTarget = await pluginModule.resolveRedisTeamTarget(
    { teamId: "75", memberId: "leader", sharedDir: shared },
    "clawmanager-monitor",
  );
  assert.equal(controlTarget.route, "control");
  assert.equal(controlTarget.completion, false);
	const decoratedLeaderTarget = await pluginModule.resolveRedisTeamTarget(
		{ teamId: "75", memberId: "developer", sharedDir: shared },
		"user:leader",
	);
	assert.equal(decoratedLeaderTarget.route, "member");
	assert.equal(decoratedLeaderTarget.to, "leader", "decorated identity is resolved from roster information, not a prefix table");
	const wrappedDeveloperTarget = await pluginModule.resolveRedisTeamTarget(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		"recipient=[developer]",
	);
	assert.equal(wrappedDeveloperTarget.to, "developer");
	const ambiguousRosterTarget = await pluginModule.resolveRedisTeamTarget(
		{ teamId: "75", memberId: "leader", sharedDir: shared },
		"ask developer, then report to leader",
	);
	assert.equal(ambiguousRosterTarget.route, "unknown");
	assert.deepEqual(ambiguousRosterTarget.targetCandidates.sort(), ["developer", "leader"]);
	const typoRosterTarget = await pluginModule.resolveRedisTeamTarget(
		{ teamId: "75", memberId: "developer", sharedDir: shared },
		"leadr",
	);
	assert.equal(typoRosterTarget.route, "unknown", "a fuzzy suggestion must never silently route a Team message");
	assert.deepEqual(typoRosterTarget.targetSuggestions, ["leader"]);
  await seedActive("leader", "leader", "leader-final-synthesis");
  const leaderTools = createHarness("leader", "leader");
	const missingTargetResult = toolResult(await leaderTools.get("team_send").execute("missing-target", {
		text: "Assign the next task.",
	}));
	assert.equal(missingTargetResult.ok, false);
	assert.equal(missingTargetResult.code, "ambiguous_team_target");
	assert.equal(missingTargetResult.sent, false, "ambiguous repair must have zero transport side effects");
  const plan = toolResult(await leaderTools.get("team_artifact_write").execute("plan-write", {
    scope: "team",
    kind: "plan",
    path: "collaboration-plan.md",
    content: "# Collaboration plan\n\nDeveloper then Reviewer.\n",
  }));
  assert.equal(plan.ok, true);
  assert.equal(plan.artifact.path, "/team/results/team-75-task-150/plan/collaboration-plan.md");
  const context = toolResult(await leaderTools.get("team_artifact_write").execute("context-write", {
    scope: "team",
    kind: "context",
    path: "issue-146.md",
    content: "# Issue 146 snapshot\n",
  }));
  assert.equal(context.ok, true);
  assert.equal(context.artifact.path, "/team/results/team-75-task-150/context/issue-146.md");
  assert.equal(
    pluginModule.canonicalArtifactAlias(
      { sharedDir: shared },
      "/team/plan/collaboration-plan.md",
      "team-75-task-150",
    ),
    "/team/results/team-75-task-150/plan/collaboration-plan.md",
    "legacy plan aliases must resolve inside the current root task only",
  );
  const assignedWorkspace = pluginModule.sharedWorkspaceForTarget(
    { sharedDir: shared, memberId: "leader" },
    {},
    "developer",
    "team-75-task-150",
    "dev-01",
  );
  assert.equal(
    assignedWorkspace.assignmentArtifactCanonicalRoot,
    "/team/artifacts/team-75-task-150/members/developer/dev-01",
  );
  assert.equal(
    assignedWorkspace.taskWorkCanonicalRoot,
    "/team/work/team-75-task-150",
    "cross-member mutable inputs must be isolated by root task",
  );
  assert.equal(
    assignedWorkspace.taskContextCanonicalRoot,
    "/team/results/team-75-task-150/context",
    "durable research context must remain inside the current root task",
  );
  const persistedLeaderEnvelope = JSON.parse(await fs.readFile(
    path.join(state, "teams", "75", "leader", "tasks", "team-75-task-150.json"),
    "utf8",
  ));
  assert.deepEqual(
    persistedLeaderEnvelope.artifactRefs,
    [plan.artifact.path, context.artifact.path],
    "Leader plan and research context refs must survive tool turns in the persisted root envelope",
  );
  const upstreamRef = "/team/artifacts/team-75-task-150/members/researcher/research-01/result.md";
  await fs.mkdir(path.join(shared, "artifacts", "team-75-task-150", "members", "researcher", "research-01"), { recursive: true });
  await fs.writeFile(
    path.join(shared, upstreamRef.slice("/team/".length)),
    "# Developer result\n",
    "utf8",
  );
  const mergedLeaderEnvelope = await pluginModule.mergeTaskEnvelopeArtifactContext(
    { teamId: "75", memberId: "leader", sharedDir: shared },
    persistedLeaderEnvelope,
    [upstreamRef],
  );
  assert.deepEqual(
    mergedLeaderEnvelope.artifactRefs,
    [plan.artifact.path, context.artifact.path, upstreamRef],
    "context-only member results must accumulate without replacing the persisted Leader plan",
  );
  const legacyPlanWrite = toolResult(await leaderTools.get("team_artifact_write").execute("plan-write-legacy", {
    scope: "team",
    kind: "plan",
    path: "plan/legacy-plan.md",
    content: "# Legacy-compatible plan path\n",
  }));
  assert.equal(legacyPlanWrite.ok, true);
  assert.equal(
    legacyPlanWrite.artifact.path,
    "/team/results/team-75-task-150/plan/legacy-plan.md",
    "legacy kind=plan writes must not create a duplicated plan/plan directory",
  );

  await seedActive("developer", "developer", "dev-01");
  const developerTools = createHarness("developer", "developer");
  const canonicalPlanRead = toolResult(await developerTools.get("team_artifact_read").execute("plan-read-canonical", {
    path: plan.artifact.path,
  }));
  assert.equal(canonicalPlanRead.ok, true);
  assert.match(canonicalPlanRead.artifact.content, /Developer then Reviewer/);
  const legacyPlanRead = toolResult(await developerTools.get("team_artifact_read").execute("plan-read-legacy", {
    scope: "team",
    kind: "plan",
    path: "plan/collaboration-plan.md",
  }));
  assert.equal(legacyPlanRead.ok, true, "kind=plan must not duplicate a leading plan/ segment");
  assert.equal(legacyPlanRead.artifact.path, plan.artifact.path);
  const teamRelativePlanRead = toolResult(await developerTools.get("team_artifact_read").execute("plan-read-relative", {
    scope: "team",
    kind: "plan",
    path: "results/team-75-task-150/plan/collaboration-plan.md",
  }));
  assert.equal(teamRelativePlanRead.ok, true, "legacy Team-relative canonical paths must remain readable");

  const normalizedEnvelope = pluginModule.normalizeEnvelope({
    teamId: "75",
    memberId: "developer",
    taskId: "team-75-task-150",
    artifactRefs: [plan.artifact.path],
    contextRefs: ["design-notes"],
  });
  assert.deepEqual(normalizedEnvelope.artifactRefs, [plan.artifact.path]);
  const guidedPrompt = pluginModule.appendRedisTeamCompletionGuidance("Implement the page.", normalizedEnvelope);
  assert.match(guidedPrompt, /read these exact canonical paths/);
  assert.match(guidedPrompt, /\/team\/results\/team-75-task-150\/plan\/collaboration-plan\.md/);
	assert.match(guidedPrompt, /call team_complete_task once/i);
	assert.match(guidedPrompt, /Production-only work should produce and hand off the artifact without tests/i);
	assert.match(guidedPrompt, /explicitly marked validationAssignment/i);
	assert.match(guidedPrompt, /never blocks delivery/i);
	const productionOnlyPrompt = pluginModule.appendRedisTeamCompletionGuidance("Implement the page.", {
		...normalizedEnvelope,
		reviewRequired: true,
	});
	assert.match(productionOnlyPrompt, /production-only work and independent validation is assigned downstream/i);
	assert.match(productionOnlyPrompt, /without running syntax checks, tests, Browser acceptance/i);
	const productionOnlyReviewerPrompt = pluginModule.appendRedisTeamCompletionGuidance("Implement the page.", {
		...normalizedEnvelope,
		role: "reviewer",
		reviewRequired: true,
	});
	assert.match(productionOnlyReviewerPrompt, /production-only work and independent validation is assigned downstream/i);
	const assignedValidatorPrompt = pluginModule.appendRedisTeamCompletionGuidance("Validate the page.", {
		...normalizedEnvelope,
		role: "domain-specialist",
		validationAssignment: true,
		validationTargetAssignmentId: "dev-01",
	});
	assert.match(assignedValidatorPrompt, /Code review policy/i);
  assert.deepEqual(
    pluginModule.canonicalTeamArtifactRefsFromText(
      { sharedDir: shared },
      `env: $CLAWMANAGER_TEAM_SHARED_DIR/index.html physical: ${path.join(shared, "index.html")}`,
    ),
    ["/team/index.html"],
    "current-Team env and physical paths must normalize to one canonical artifact ref",
  );
  assert.deepEqual(
    pluginModule.canonicalTeamArtifactRefsFromText(
      { sharedDir: shared },
      "/workspaces/teams/user-1/team-999-shared/private.html",
    ),
    [],
    "a sibling Team physical path must never be imported into the current Team context",
  );

  const progress = toolResult(await developerTools.get("team_update_progress").execute("progress-1", {
    status: "running",
    progress: 25,
    eventKind: "worker_plan",
    summary: "\u5f00\u59cb\u5b9e\u73b0\u8f7b\u91cf\u770b\u677f\u9875\u9762",
  }));
  assert.equal(progress.ok, true);
  assert.equal(progress.status.currentTaskId, "team-75-task-150");
  assert.equal(progress.status.currentAssignmentId, "dev-01");
  const englishProgress = toolResult(await developerTools.get("team_update_progress").execute("progress-en", {
    status: "running",
    progress: 30,
    eventKind: "worker_progress",
    summary: "Running static checks before delivery",
  }));
  assert.equal(englishProgress.ok, true, "locale mismatch must remain non-blocking");

  await fs.writeFile(path.join(shared, "index.html"), "<!doctype html><title>Team 75</title>", "utf8");
  const preview = toolResult(await developerTools.get("team_artifact_preview").execute("preview-index", {
    scope: "team",
    path: "index.html",
  }));
  assert.equal(preview.ok, true);
  assert.equal(preview.artifact.path, "/team/index.html");
  assert.match(
    preview.artifact.previewUrl,
    /^http:\/\/clawmanager-egress-proxy\.clawmanager-hxc-peer-system\.svc\.cluster\.local:3128\/v2\/interactive\/75\/_\/[A-Za-z0-9_-]+\/index\.html$/,
  );
	assert.equal(new URL(preview.artifact.previewUrl).port, "3128", "interactive previews bootstrap through the resolvable managed proxy");
  assert.equal(new URL(preview.artifact.previewUrl).search, "", "preview links must not carry an expiry");
  process.env.CLAWMANAGER_TEAM_PREVIEW_ORIGIN = "http://clawmanager-team-preview.invalid";
  const legacyPreviewUrl = pluginModule.previewUrlForTeamArtifact(
    { teamId: "75", sharedDir: shared },
    path.join(shared, "notes.txt"),
  );
  assert.equal(
    new URL(legacyPreviewUrl.url).hostname,
    "clawmanager-egress-proxy.clawmanager-hxc-peer-system.svc.cluster.local",
    "a new Runtime must repair the legacy .invalid origin with the managed proxy address",
  );
  assert.equal(new URL(legacyPreviewUrl.url).port, "3128");
  process.env.CLAWMANAGER_TEAM_PREVIEW_ORIGIN = "http://attacker.example";
  assert.throws(
    () => pluginModule.previewUrlForTeamArtifact(
      { teamId: "75", sharedDir: shared },
      path.join(shared, "index.html"),
    ),
    /not managed by ClawManager/,
    "an arbitrary preview origin must never receive a Team-signed artifact path",
  );
  process.env.CLAWMANAGER_TEAM_PREVIEW_ORIGIN = "http://clawmanager-egress-proxy.clawmanager-hxc-peer-system.svc.cluster.local:3128";
  const truncatedRead = toolResult(await developerTools.get("team_artifact_read").execute("read-truncated", {
    scope: "team",
    path: "index.html",
    maxBytes: 10,
  }));
  assert.equal(truncatedRead.ok, true);
  assert.equal(truncatedRead.artifact.truncated, true);
  assert.equal(truncatedRead.artifact.nextOffset, 10);
  assert.equal(Buffer.byteLength(truncatedRead.artifact.content, "utf8"), 10);
  const completion = toolResult(await developerTools.get("team_complete_task").execute("complete-1", {
    status: "succeeded",
    summary: "\u8f7b\u91cf\u770b\u677f\u7f51\u9875\u5f00\u53d1\u5b8c\u6210",
    resultMarkdown: "\u4ea4\u4ed8\u6587\u4ef6\uff1a/team/index.html",
  }));
  assert.deepEqual(completion.artifactRefs, ["/team/index.html"]);

  const developerStatusPath = path.join(shared, "status", "developer.json");
  const acceptedStatus = {
    teamId: "75",
    memberId: "developer",
    role: "developer",
    currentTaskId: "team-75-task-150",
    currentAssignmentId: "dev-01",
    runtimeStatus: "succeeded",
    availability: "idle",
    progress: 100,
    lastSummary: "Development result accepted",
    artifactRefs: ["/team/index.html"],
    resultContentHash: resultContentHash("\u4ea4\u4ed8\u6587\u4ef6\uff1a/team/index.html", ["/team/index.html"]),
  };
  await fs.writeFile(developerStatusPath, JSON.stringify(acceptedStatus), "utf8");
  const lateProgress = toolResult(await developerTools.get("team_update_progress").execute("progress-late", {
    status: "running",
    progress: 99,
    eventKind: "worker_progress",
    summary: "\u8fdf\u5230\u7684\u8fd0\u884c\u72b6\u6001",
  }));
  assert.equal(lateProgress.status.runtimeStatus, "succeeded");
  assert.equal(lateProgress.status.lastSummary, "Development result accepted");
  assert.equal(JSON.parse(await fs.readFile(developerStatusPath, "utf8")).lastSummary, "Development result accepted");
  const legacyAcceptedStatus = { ...acceptedStatus };
  delete legacyAcceptedStatus.resultContentHash;
  await fs.writeFile(developerStatusPath, JSON.stringify(legacyAcceptedStatus), "utf8");
  const legacyChangedCompletion = toolResult(await developerTools.get("team_complete_task").execute("complete-legacy-changed", {
    status: "succeeded",
    summary: "\u65e7\u7248\u72b6\u6001\u4e0b\u7684\u4fee\u6b63",
    resultMarkdown: "\u65e7\u7248\u72b6\u6001\u4e0b\u4e0d\u5e94\u731c\u6d4b\u662f\u5426\u9700\u8981\u91cd\u5f00\uff1a/team/index.html",
  }));
  assert.equal(legacyChangedCompletion.completion.reason, "already_terminal");
  assert.equal(legacyChangedCompletion.completion.published, false);
  await fs.writeFile(developerStatusPath, JSON.stringify(acceptedStatus), "utf8");
  const duplicateCompletion = toolResult(await developerTools.get("team_complete_task").execute("complete-duplicate", {
    status: "succeeded",
    summary: "\u8f7b\u91cf\u770b\u677f\u7f51\u9875\u5f00\u53d1\u5b8c\u6210",
    resultMarkdown: "\u4ea4\u4ed8\u6587\u4ef6\uff1a/team/index.html",
  }));
  assert.equal(duplicateCompletion.completion.reason, "already_terminal");
  assert.equal(duplicateCompletion.completion.published, false);
  const correctedCompletion = toolResult(await developerTools.get("team_complete_task").execute("complete-corrected", {
    status: "succeeded",
    summary: "\u8f7b\u91cf\u770b\u677f\u7f51\u9875\u4fee\u6b63\u5b8c\u6210",
    resultMarkdown: "\u4fee\u6b63\u540e\u4ea4\u4ed8\u6587\u4ef6\uff1a/team/index.html",
  }));
  assert.equal(correctedCompletion.status.runtimeStatus, "completion_pending");

  await seedActive("reviewer", "reviewer", "review-01");
  const reviewerTools = createHarness("reviewer", "reviewer");
  const review = toolResult(await reviewerTools.get("team_artifact_write").execute("review-1", {
    scope: "team",
    kind: "review",
    path: "review-report.md",
    content: "# Review report\n\nPASS\n",
  }));
  assert.equal(review.ok, true);
  assert.equal(review.artifact.path, "/team/results/team-75-task-150/reviews/review-01/review-report.md");
  await fs.access(path.join(shared, "results", "team-75-task-150", "reviews", "review-01", "review-report.md"));
  const inferredCanonicalReview = toolResult(await reviewerTools.get("team_artifact_write").execute("review-canonical", {
    path: "/team/results/team-75-task-150/reviews/review-01/canonical-review-report.md",
    content: "# Canonical review report\n\nPASS\n",
  }));
  assert.equal(
    inferredCanonicalReview.artifact.path,
    "/team/results/team-75-task-150/reviews/review-01/canonical-review-report.md",
    "the active Reviewer canonical path must infer the non-blocking review contract",
  );
  const normalizedStaleReviewPath = toolResult(
    await reviewerTools.get("team_artifact_write").execute("review-wrong-assignment", {
      path: "/team/results/team-75-task-150/reviews/review-02/wrong-review-report.md",
      content: "# Normalized stale path\n\nPASS\n",
    }),
  );
  assert.equal(
    normalizedStaleReviewPath.artifact.path,
    "/team/results/team-75-task-150/reviews/review-01/wrong-review-report.md",
    "a stale target/retry directory must normalize into the active validation assignment",
  );
  const normalizedReviewDirectory = toolResult(
    await reviewerTools.get("team_artifact_mkdir").execute("review-mkdir-wrong-assignment", {
      path: "/team/results/team-75-task-150/reviews/dev-01",
    }),
  );
  assert.equal(
    normalizedReviewDirectory.artifact.path,
    "/team/results/team-75-task-150/reviews/review-01",
    "review directory creation must use the same tolerant canonical lane as file writes",
  );
  const legacyPrefixedReview = toolResult(await reviewerTools.get("team_artifact_write").execute("review-prefixed", {
    scope: "team",
    kind: "review",
    path: "review-01/legacy-review-report.md",
    content: "# Legacy review report\n\nPASS\n",
  }));
  assert.equal(
    legacyPrefixedReview.artifact.path,
    "/team/results/team-75-task-150/reviews/review-01/legacy-review-report.md",
    "kind=review must strip exactly one duplicated active assignment prefix",
  );
	const legacyMemberReport = toolResult(await reviewerTools.get("team_artifact_write").execute("review-member-report", {
		scope: "member",
		path: "QA-REPORT.md",
		content: "# QA Report\n\nPASS\n",
	}));
	assert.equal(
		legacyMemberReport.artifact.path,
		"/team/artifacts/team-75-task-150/members/reviewer/review-01/QA-REPORT.md",
	);
	const legacyMemberEvidence = toolResult(await reviewerTools.get("team_artifact_write").execute("review-member-evidence", {
		scope: "member",
		path: "EVIDENCE.json",
		content: '{"pass":true}\n',
	}));
	const normalizedReviewCompletion = toolResult(await reviewerTools.get("team_complete_task").execute("review-complete", {
		status: "succeeded",
		summary: "\u5ba1\u6838\u5b8c\u6210",
		resultMarkdown: `\u5ba1\u6838\u62a5\u544a\uff1a${legacyMemberReport.artifact.path}`,
		artifactRefs: [legacyMemberReport.artifact.path, legacyMemberEvidence.artifact.path],
	}));
	assert.ok(
		normalizedReviewCompletion.artifactRefs.includes("/team/results/team-75-task-150/reviews/review-01/QA-REPORT.md"),
		"an explicitly referenced legacy member report must be copied to the canonical review root",
	);
	assert.ok(
		normalizedReviewCompletion.artifactRefs.includes("/team/results/team-75-task-150/reviews/review-01/EVIDENCE.json"),
		"multiple explicit review evidence files must mirror without creating a completion retry",
	);
	await fs.access(path.join(shared, "results", "team-75-task-150", "reviews", "review-01", "QA-REPORT.md"));
  const tolerantReviewRead = toolResult(await reviewerTools.get("team_artifact_read").execute("review-read-stale-path", {
    scope: "team",
    path: "/team/results/team-75-task-150/reviews/old-review-assignment/QA-REPORT.md",
  }));
  assert.equal(tolerantReviewRead.ok, true);
  assert.equal(
    tolerantReviewRead.artifact.path,
    "/team/results/team-75-task-150/reviews/review-01/QA-REPORT.md",
    "a unique report in the current root work must remain readable through a stale assignment path",
  );
	await seedActive("reviewer", "reviewer", "review-01", {
		revision: 2,
		validationAssignment: true,
		validationTargetAssignmentId: "dev-01",
		validationTargetRevision: 2,
	});
	const revisionTwoReview = toolResult(await reviewerTools.get("team_artifact_write").execute("review-r2", {
		scope: "team",
		kind: "review",
		path: "/team/results/team-75-task-150/reviews/review-01/review-r2.md",
		content: "# Review revision 2\n\nPASS\n",
	}));
	assert.equal(
		revisionTwoReview.artifact.path,
		"/team/results/team-75-task-150/reviews/review-01/revisions/r2/review-r2.md",
		"each validation revision must retain an independent canonical report directory",
	);
	const revisionTwoMemberReport = toolResult(await reviewerTools.get("team_artifact_write").execute("review-r2-member", {
		scope: "member",
		path: "QA-REPORT.md",
		content: "# Review revision 2 member report\n\nPASS\n",
	}));
	assert.equal(
		revisionTwoMemberReport.artifact.path,
		"/team/artifacts/team-75-task-150/members/reviewer/review-01/revisions/r2/QA-REPORT.md",
	);
	await fs.writeFile(path.join(state, "teams", "75", "reviewer", "status.json"), JSON.stringify({
		availability: "busy",
		runtimeStatus: "running",
		currentTaskId: "team-75-task-150",
		currentAssignmentId: "review-01",
		currentRevision: 2,
		lastSeenAt: new Date().toISOString(),
	}), "utf8");
	const revisionTwoCompletion = toolResult(await reviewerTools.get("team_complete_task").execute("review-r2-complete", {
		status: "succeeded",
		summary: "Review revision 2 complete",
		resultMarkdown: `Report: ${revisionTwoMemberReport.artifact.path}`,
		artifactRefs: [revisionTwoMemberReport.artifact.path],
	}));
	assert.ok(
		revisionTwoCompletion.artifactRefs.includes("/team/results/team-75-task-150/reviews/review-01/revisions/r2/QA-REPORT.md"),
		"completion mirrors a tolerant member report into the same revision-specific canonical directory",
	);
	assert.equal(
		pluginModule.validationRevisionDirectory(
			{ memberId: "reviewer", role: "reviewer" },
			{ revision: 3, validationAssignment: true },
		),
		path.join("revisions", "r3"),
	);
  await seedActive("auditor", "domain-specialist", "audit-01", {
    validationAssignment: true,
    validationTargetAssignmentId: "dev-01",
    validationTargetRevision: 1,
  });
  const auditorTools = createHarness("auditor", "domain-specialist");
  const genericValidationReport = toolResult(await auditorTools.get("team_artifact_write").execute("audit-report", {
    scope: "team",
    kind: "review",
    path: "validation-report.md",
    content: "# Validation report\n\nPASS\n",
  }));
  assert.equal(
    genericValidationReport.artifact.path,
    "/team/results/team-75-task-150/reviews/audit-01/validation-report.md",
    "an explicitly assigned validator must not depend on a Reviewer role name",
  );
  const inferredGenericValidationReport = toolResult(await auditorTools.get("team_artifact_write").execute("audit-canonical", {
    path: "/team/results/team-75-task-150/reviews/audit-01/canonical-validation-report.md",
    content: "# Canonical validation report\n\nPASS\n",
  }));
  assert.equal(
    inferredGenericValidationReport.artifact.path,
    "/team/results/team-75-task-150/reviews/audit-01/canonical-validation-report.md",
    "an assigned validator canonical path must infer review scope without relying on its role name",
  );
  await seedActive("developer", "developer", "dev-02");
  const scopedDeveloperTools = createHarness("developer", "developer");
  await assert.rejects(
    () => scopedDeveloperTools.get("team_artifact_write").execute("developer-review-path", {
      path: "/team/results/team-75-task-150/reviews/audit-01/developer-review.md",
      content: "must not be written",
    }),
    /outside the active artifact scope/,
    "a normal member must not inherit the validation writer path contract",
  );

  await seedActive("leader", "leader", "review-01");
  const leaderIdentityTools = createHarness("leader", "leader");
  const leaderSynthesis = toolResult(await leaderIdentityTools.get("team_update_progress").execute("leader-synthesis", {
    status: "running",
    progress: 95,
    eventKind: "leader_synthesis",
    summary: "\u6b63\u5728\u6574\u7406\u6700\u7ec8\u4ea4\u4ed8",
  }));
  assert.equal(
    leaderSynthesis.status.currentAssignmentId,
    "leader-final-synthesis",
    "Leader synthesis must not inherit the Reviewer's assignment identity",
  );

  const staleWorkspaceEnvelope = pluginModule.normalizeEnvelope({
    taskId: "team-28-task-64",
    rootTaskId: "team-28-task-64",
    to: "delivery-lead",
    prompt: "Continue task 64; the conversation may still discuss team-28-task-63.",
    workspaceContract: {
      physicalSharedDir: "/workspaces/teams/user-1/team-28-shared",
      taskRef: "team-28-task-63",
      artifactRoot: "/team/artifacts/team-28-task-63",
      extensionField: "preserved",
    },
    sharedWorkspace: {
      physicalPath: "/workspaces/teams/user-1/team-28-shared",
      taskWorkCanonicalRoot: "/team/work/team-28-task-63",
    },
  });
  assert.equal(staleWorkspaceEnvelope.workspaceContract.taskRef, "team-28-task-64");
  assert.equal(staleWorkspaceEnvelope.workspaceContract.artifactRoot, "/team/artifacts/team-28-task-64");
  assert.equal(staleWorkspaceEnvelope.workspaceContract.extensionField, "preserved");
  assert.equal(staleWorkspaceEnvelope.sharedWorkspace.taskWorkCanonicalRoot, "/team/work/team-28-task-64");
  assert.match(staleWorkspaceEnvelope.text, /team-28-task-63/, "historical conversation text must remain intact");

  const proseArtifactRefs = pluginModule.canonicalTeamArtifactRefsFromText(
    { sharedDir: shared },
    [
      "/team/results/team-28-task-64/plan/collaboration-plan.md，随后派发任务",
      "/team/artifacts/team-28-task-64/members/developer/dev-board/index.html（40786 字节）",
      "/team/results/team-28-task-64/reviews/review-board/review-report.md（zh-CN），含结论",
      "/team/results/team-28-task-64/reviews/review-board/，并继续",
    ].join("\n"),
    "team-28-task-64",
  );
  assert.deepEqual(proseArtifactRefs, [
    "/team/results/team-28-task-64/plan/collaboration-plan.md",
    "/team/artifacts/team-28-task-64/members/developer/dev-board/index.html",
    "/team/results/team-28-task-64/reviews/review-board/review-report.md",
  ]);

  const inferredReviewDirectory = pluginModule.inferCanonicalArtifactWriteContract(
    { memberId: "reviewer", role: "reviewer" },
    { path: "/team/results/team-75-task-150/reviews/dev-01" },
    {
      rootTaskId: "team-75-task-150",
      assignmentId: "review-01",
      validationAssignment: true,
      validationTargetAssignmentId: "dev-01",
    },
  );
  assert.deepEqual(inferredReviewDirectory, {
    path: "/team/results/team-75-task-150/reviews/review-01",
    scope: "team",
    kind: "review",
  });

  const monotonicPreviewUrl = "http://p-abcdefghijklmnop.clawmanager-team-preview.invalid/v2/interactive/test/index.html";
  const openEvidence = pluginModule.reviewerBrowserToolResultDecision(
    browserEnvelope,
    { toolName: "browser", params: { action: "open", url: monotonicPreviewUrl }, result: { targetId: "target-1" } },
    {},
    1000,
  );
  assert.equal(openEvidence.managedPreviewOpened, true, "a successful open is evidence even if pending in-memory state was lost");
  const laterSnapshotFailure = pluginModule.reviewerBrowserToolResultDecision(
    browserEnvelope,
    { toolName: "browser", params: { action: "snapshot" }, error: "ENOTFOUND" },
    openEvidence,
    1100,
  );
  const monotonicEvidence = pluginModule.mergeBrowserVerificationState(openEvidence, laterSnapshotFailure);
  assert.equal(monotonicEvidence.managedPreviewOpened, true, "a later Browser error must not erase a successful open");
  assert.equal(monotonicEvidence.managedPreviewInspected, undefined);

	const freshStatusAt = new Date().toISOString();
	const activeRecoveryDecision = pluginModule.decideBusinessDelivery({
		roster: {
			members: [
				{ memberId: "leader", aliases: ["leader"], isLeader: true },
				{ memberId: "developer", aliases: ["developer"], isLeader: false },
			],
			raw: { communicationMode: "leader_mediated" },
		},
		message: {
			from: "leader",
			to: "developer",
			rootTaskId: "team-75-task-150",
			assignmentId: "dev-01",
			workId: "dev-01",
			intent: "send",
		},
		sourceEnvelope: { from: "clawmanager", intent: "assignment_recovery_request", assignmentId: "dev-01" },
		workflowState: {
			assignments: {
				"dev-01": { assignmentId: "dev-01", ownerMemberKey: "developer", revision: 1, status: "failed", nextRevisionAllowed: true, nextRevision: 2 },
			},
		},
		targetStatus: {
			runtimeStatus: "awaiting_completion_receipt",
			availability: "busy",
			lastSeenAt: freshStatusAt,
			currentTaskId: "team-75-task-150",
			currentAssignmentId: "dev-01",
			currentRevision: 1,
		},
		explicitWorkId: false,
		recentTargetDispatch: null,
	});
	assert.equal(activeRecoveryDecision.kind, "context");
	assert.equal(activeRecoveryDecision.reason, "runtime_attempt_still_active");
	assert.equal(activeRecoveryDecision.revision, 1, "a transport/projection conflict must stay on the live attempt instead of manufacturing r2");
	assert.equal(pluginModule.equivalentActiveAssignment({
		runtimeStatus: "running",
		availability: "busy",
		lastSeenAt: freshStatusAt,
		currentTaskId: "team-75-task-150",
		currentAssignmentId: "review-01",
		currentRevision: 2,
		currentValidationTargetAssignmentId: "dev-01",
		currentValidationTargetRevision: 2,
	}, {
		rootTaskId: "team-75-task-150",
		assignmentId: "review-01",
		revision: 3,
		validationTargetAssignmentId: "dev-01",
		validationTargetRevision: 2,
	}), false, "a different requested revision is not the same active attempt and must reach business-delivery reconciliation");
	assert.equal(pluginModule.equivalentActiveAssignment({
		runtimeStatus: "running",
		availability: "busy",
		lastSeenAt: freshStatusAt,
		currentTaskId: "team-75-task-old",
		currentAssignmentId: "review-01",
		currentRevision: 2,
	}, {
		rootTaskId: "team-75-task-150",
		assignmentId: "review-01",
		revision: 2,
	}), false, "an active assignment from another root task must never suppress a new dispatch");
	assert.ok(pluginModule.equivalentWorkflowAttempt({ assignments: {
		"review-01": {
			assignmentId: "review-01",
			revision: 2,
			status: "running",
			validationTargetAssignmentId: "dev-01",
			validationTargetRevision: 2,
			updatedAt: freshStatusAt,
		},
	} }, {
		assignmentId: "review-01",
		revision: 3,
		validationTargetAssignmentId: "dev-01",
		validationTargetRevision: 2,
	}), "the durable ledger must close the status-propagation race without creating another Work Item");

  assert.match(source, /function analyzeResponseLocale\(/);
  assert.doesNotMatch(source, /must use \$\{locale \|\| "zh-CN"\}/);
  assert.match(source, /workflowReminderIsStale/);
  assert.match(source, /ignored message post-processing failure after terminal assignment/);
  assert.doesNotMatch(source, /automatic_turn_completion_v2/);
  assert.match(source, /explicit_completion_receipt_v1/);
  assert.match(source, /turn_end_monitor_v1/);
  assert.match(source, /validation_contract_v2/);

  console.log("Team75 Redis Team contract test passed");
} finally {
  // Windows may release handles created by the contract harness a moment after
  // the final assertion. Retry transient ENOTEMPTY/EPERM cleanup failures so
  // test status continues to reflect contract behavior rather than OS timing.
  await fs.rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}
