import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const redisUrl = process.env.REDIS_TEAM_TEST_URL;
if (!redisUrl) throw new Error("REDIS_TEAM_TEST_URL is required");

const root = await fs.mkdtemp(path.join(os.tmpdir(), "redis-team-dispatch-"));
const state = path.join(root, "state");
const shared = path.join(root, "shared");
process.env.XDG_STATE_HOME = state;

const distPath = path.resolve(import.meta.dirname, "..", "dist", "index.js");
const source = (await fs.readFile(distPath, "utf8"))
  .replace('import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";', "const definePluginEntry = (entry) => entry;")
  .replace('import { dispatchInboundDirectDmWithRuntime } from "openclaw/plugin-sdk/direct-dm";', "const dispatchInboundDirectDmWithRuntime = async () => ({});");
const testSource = source + "\nexport { createRuntime, RedisClient, rootWorkflowStateKey };\n";
const pluginModule = await import(`data:text/javascript;base64,${Buffer.from(testSource).toString("base64")}`);

const teamId = "501";
const rootTaskId = "team-501-task-1";
const prefix = `claw:team:${teamId}`;
const config = {
  channels: {
    "redis-team": {
      accounts: {
        default: {
          fromEnv: false,
          enabled: true,
          redisUrl,
          teamId,
          memberId: "leader",
          role: "leader",
          sharedDir: shared,
        },
      },
    },
  },
};
const cfg = config.channels["redis-team"].accounts.default;
const runtime = pluginModule.createRuntime({ config, logger: { warn() {}, info() {}, error() {} } });
const redis = new pluginModule.RedisClient(redisUrl);

try {
  await fs.mkdir(shared, { recursive: true });
  await fs.writeFile(path.join(shared, "team.json"), JSON.stringify({
    teamId,
    communicationMode: "leader_mediated",
    members: [
      { memberId: "leader", role: "leader", isLeader: true },
      { memberId: "developer", role: "developer" },
      { memberId: "reviewer", role: "reviewer" },
      { memberId: "researcher", role: "researcher" },
    ],
  }), "utf8");
  await redis.connect();
  await redis.command(
    "DEL",
    `${prefix}:inbox:developer`,
    `${prefix}:inbox:reviewer`,
    `${prefix}:inbox:researcher`,
    `${prefix}:events`,
    `${prefix}:root:${rootTaskId}:state`,
    `${prefix}:root:${rootTaskId}:assignment-dispatch`,
    `${prefix}:root:${rootTaskId}:deferred:leader`,
    `${prefix}:deferred-roots:leader`,
  );

  const envelope = {
    v: 4,
    protocolVersion: 4,
    teamId,
    memberId: "leader",
    role: "leader",
    taskId: rootTaskId,
    rootTaskId,
    rootMessageId: "root-message-1",
    messageId: "root-message-1",
    assignmentId: "leader-final-synthesis",
    workId: "leader-final-synthesis",
    requiresCompletion: true,
  };

  await runtime.withActiveEnvelope(envelope, async () => {
    const normalizedMissingIdentity = await runtime.send({
      to: "researcher",
      text: "Collect the primary source evidence.",
      intent: "assignment",
    });
    assert.equal(normalizedMissingIdentity.businessDeliveryKind, "assignment");
    assert.equal(normalizedMissingIdentity.revision, 1);
    const racingMissingIdentity = await runtime.send({
      to: "researcher",
      text: "Also include a concise risk summary.",
      intent: "send",
    });
    assert.equal(racingMissingIdentity.businessDeliveryKind, "ambiguous");
    assert.equal(racingMissingIdentity.sent, true);
    assert.equal(racingMissingIdentity.businessDeliveryReason, "target_has_recent_unprojected_assignment");
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:researcher`)), 2, "the follow-up remains visible without creating a second business contract");

    const developer = await runtime.send({
      to: "developer",
      text: "Implement the page.",
      intent: "assignment",
      assignmentId: "dev-page",
      workId: "dev-page",
      phaseId: "phase-dev",
      revision: 1,
      required: true,
    });
    assert.equal(developer.sent, undefined, "a normal dispatch keeps the established message envelope contract");
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:developer`)), 1);

    const dependent = await runtime.send({
      to: "reviewer",
      text: "Review the page after development.",
      intent: "assignment",
      assignmentId: "review-page",
      workId: "review-page",
      phaseId: "phase-review",
      revision: 1,
      required: true,
      dependsOn: ["dev-page"],
      validationAssignment: true,
      validationTargetAssignmentId: "dev-page",
      validationTargetRevision: 1,
    });
    assert.equal(dependent.sent, true);
    assert.equal(dependent.deferred, false);
    assert.equal(dependent.deliveryState, "dispatched_with_dependency_advisory");
    assert.equal(dependent.dependencyAdvisory.state, "known_waiting");
    assert.deepEqual(dependent.dependencyAdvisory.waiting, ["dev-page"]);
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:reviewer`)), 1, "dependency metadata must not create a hidden execution lock");

    const repeatedWaiting = await runtime.send({
      to: "reviewer",
      text: "Review the page after development; include accessibility.",
      intent: "assignment",
      assignmentId: "review-page",
      workId: "review-page",
      phaseId: "phase-review",
      revision: 1,
      required: true,
      dependsOn: ["dev-page"],
      validationAssignment: true,
      validationTargetAssignmentId: "dev-page",
      validationTargetRevision: 1,
    });
    assert.equal(repeatedWaiting.deduplicated, true);
    assert.equal(repeatedWaiting.deferred, false);
    assert.equal(repeatedWaiting.reason, "already_in_progress");
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:reviewer`)), 1, "Leader retries must not create a second execution");

    await redis.command("SET", pluginModule.rootWorkflowStateKey(cfg, rootTaskId), JSON.stringify({
      status: "running",
      terminal: false,
      assignments: {
        "dev-page": { assignmentId: "dev-page", revision: 1, status: "succeeded", updatedAt: new Date().toISOString() },
      },
    }));
    const releasedRequest = {
      to: "reviewer",
      text: "Review the page after development; include accessibility.",
      intent: "assignment",
      assignmentId: "review-page",
      workId: "review-page",
      phaseId: "phase-review",
      revision: 1,
      required: true,
      dependsOn: ["dev-page"],
      validationAssignment: true,
      validationTargetAssignmentId: "dev-page",
      validationTargetRevision: 1,
    };
    const [afterDependency, concurrentRetry] = await Promise.all([
      runtime.send(releasedRequest),
      runtime.send({ ...releasedRequest }),
    ]);
    assert.equal(afterDependency.deduplicated, true, "an already-dispatched dependent attempt remains one execution");
    assert.equal(afterDependency.reason, "already_in_progress");
    assert.equal(concurrentRetry.deduplicated, true);
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:reviewer`)), 1, "concurrent retries must keep one inbox message");

    await runtime.send({
      to: "reviewer",
      text: "Independent evidence check.",
      intent: "assignment",
      assignmentId: "independent-check",
      workId: "independent-check",
      phaseId: "phase-independent",
      revision: 1,
      required: true,
    });
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:reviewer`)), 2, "independent work must remain immediately parallel");

    const unknownDependency = await runtime.send({
      to: "reviewer",
      text: "Compatibility assignment with an unknown dependency label.",
      intent: "assignment",
      assignmentId: "unknown-dependency-check",
      workId: "unknown-dependency-check",
      phaseId: "phase-compat",
      revision: 1,
      required: true,
      dependsOn: ["legacy-natural-language-label"],
    });
    assert.equal(Number(await redis.command("XLEN", `${prefix}:inbox:reviewer`)), 3, "unknown dependency identity must fail open instead of freezing mixed versions");
		assert.equal(unknownDependency.deferred, false);
		assert.equal(unknownDependency.dependencyAdvisory.state, "unknown_advisory");

    await redis.command("SET", pluginModule.rootWorkflowStateKey(cfg, rootTaskId), JSON.stringify({
      status: "running",
      terminal: false,
      assignmentLedgerComplete: true,
      snapshotSchemaVersion: 2,
      ledgerVersion: 7,
      assignments: {
        "dev-page": {
          assignmentId: "dev-page",
          ownerMemberKey: "developer",
          revision: 1,
          status: "succeeded",
          nextRevisionAllowed: false,
          nextRevision: 2,
          updatedAt: new Date().toISOString(),
        },
        "review-page": {
          assignmentId: "review-page",
          ownerMemberKey: "reviewer",
          revision: 1,
          status: "succeeded",
          nextRevisionAllowed: false,
          nextRevision: 2,
          updatedAt: new Date().toISOString(),
        },
      },
    }));

    const completedFollowUp = await runtime.send({
      to: "developer",
      text: "Please clarify one implementation detail.",
      intent: "assignment",
      assignmentId: "dev-page",
      revision: 99,
    });
    assert.equal(completedFollowUp.businessDeliveryKind, "ambiguous");
    assert.equal(completedFollowUp.revision, 1, "Agent-authored revision numbers cannot create a successor");
    assert.equal(completedFollowUp.requiresCompletion, false);

    const nextStageSameMember = await runtime.send({
      to: "developer",
      text: "Implement the independent export stage.",
      intent: "assignment",
      assignmentId: "dev-export",
      revision: 8,
    });
    assert.equal(nextStageSameMember.businessDeliveryKind, "assignment");
    assert.equal(nextStageSameMember.assignmentId, "dev-export");
    assert.equal(nextStageSameMember.revision, 1, "a distinct multi-stage assignment starts at revision 1");

    const ambiguousSameTarget = await runtime.send({
      to: "reviewer",
      text: "Take another look and answer a question.",
      intent: "send",
    });
    assert.equal(ambiguousSameTarget.businessDeliveryKind, "ambiguous");
    assert.equal(ambiguousSameTarget.sent, true, "uncertain traffic is still delivered to preserve flow");
    assert.equal(ambiguousSameTarget.clarificationRequired, true);

    await redis.command("SET", pluginModule.rootWorkflowStateKey(cfg, rootTaskId), JSON.stringify({
      status: "running",
      terminal: false,
      assignmentLedgerComplete: true,
      snapshotSchemaVersion: 2,
      ledgerVersion: 8,
      assignments: {
        "dev-page": {
          assignmentId: "dev-page",
          ownerMemberKey: "developer",
          revision: 1,
          status: "failed",
          nextRevisionAllowed: true,
          nextRevision: 2,
          updatedAt: new Date().toISOString(),
        },
      },
    }));
		await fs.mkdir(path.join(shared, "status"), { recursive: true });
		await fs.writeFile(path.join(shared, "status", "developer.json"), JSON.stringify({
			memberId: "developer",
			runtimeStatus: "awaiting_completion_receipt",
			availability: "busy",
			lastSeenAt: new Date().toISOString(),
			currentTaskId: rootTaskId,
			currentAssignmentId: "dev-page",
			currentRevision: 1,
		}), "utf8");
		const activeAttemptRecovery = await runtime.send({
			to: "developer",
			text: "The transport receipt conflicted with your live attempt; continue the same work.",
			intent: "send",
			assignmentId: "dev-page",
		});
		assert.equal(activeAttemptRecovery.businessDeliveryKind, "context");
		assert.equal(activeAttemptRecovery.businessDeliveryReason, "runtime_attempt_still_active");
		assert.equal(activeAttemptRecovery.revision, 1, "a live r1 receives a recovery reminder instead of an invented r2");
		await fs.writeFile(path.join(shared, "status", "developer.json"), JSON.stringify({
			memberId: "developer",
			runtimeStatus: "failed",
			availability: "blocked",
			lastSeenAt: new Date(Date.now() - 10 * 60_000).toISOString(),
			currentTaskId: rootTaskId,
			currentAssignmentId: "dev-page",
			currentRevision: 1,
		}), "utf8");
    const recovery = await runtime.send({
      to: "developer",
      text: "Fix the accepted failure and redeliver.",
      intent: "send",
      assignmentId: "dev-page",
      revision: 77,
    });
    assert.equal(recovery.businessDeliveryKind, "assignment");
    assert.equal(recovery.businessDeliveryReason, "ledger_authorized_recovery");
    assert.equal(recovery.revision, 2);
    assert.equal(recovery.revisionAuthorized, true);
  }, cfg);

  console.log("Redis Team dependency supervision regression: OK");
} finally {
  redis.close();
  await fs.rm(root, { recursive: true, force: true });
}
