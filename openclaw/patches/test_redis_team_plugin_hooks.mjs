import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const openclawRoot = "/usr/local/lib/node_modules/openclaw";
const openclawDist = path.join(openclawRoot, "dist");
const installedPluginEntry = process.env.REDIS_TEAM_PLUGIN_ENTRY ||
  "/defaults/.openclaw/extensions/redis-team/dist/index.js";

const root = await fs.mkdtemp(path.join(os.tmpdir(), "redis-team-real-hook-host-"));
try {
  const pluginRoot = path.join(root, "redis-team");
  const pluginDist = path.join(pluginRoot, "dist");
  await fs.mkdir(pluginDist, { recursive: true });
  await fs.mkdir(path.join(root, "node_modules"), { recursive: true });
  await fs.symlink(openclawRoot, path.join(root, "node_modules", "openclaw"), "dir");
  await fs.copyFile(installedPluginEntry, path.join(pluginDist, "index.js"));
  await fs.writeFile(path.join(pluginRoot, "package.json"), JSON.stringify({ type: "module" }), "utf8");

  const pluginModule = await import(pathToFileURL(path.join(pluginDist, "index.js")).href);
  const agentHarness = await import(pathToFileURL(path.join(openclawDist, "plugin-sdk", "agent-harness.js")).href);
  let hookRunnerGlobal = null;
  for (const name of await fs.readdir(openclawDist)) {
    if (!name.startsWith("hook-runner-global-") || !name.endsWith(".js")) continue;
    const candidate = await import(pathToFileURL(path.join(openclawDist, name)).href);
    if (typeof candidate.i === "function" && typeof candidate.t === "function") {
      hookRunnerGlobal = candidate;
      break;
    }
  }
  assert.ok(hookRunnerGlobal, "OpenClaw hook runner module is installed");

  const sharedDir = path.join(root, "shared");
  await fs.mkdir(sharedDir, { recursive: true });
  const stateDir = path.join(root, "state");
  process.env.XDG_STATE_HOME = stateDir;
  const config = {
    channels: {
      "redis-team": {
        accounts: {
          default: {
            fromEnv: false,
            enabled: true,
            teamId: "real-hook-host",
            memberId: "leader",
            role: "leader",
            sharedDir,
          },
        },
      },
    },
  };
  const tools = new Map();
  const typedHooks = [];
  pluginModule.default.register({
    config,
    logger: { debug() {}, info() {}, warn() {}, error() {} },
    registerTool(tool) {
      tools.set(tool.name, tool);
    },
    registerChannel() {},
    on(hookName, handler, options = {}) {
      typedHooks.push({
        pluginId: "redis-team",
        hookName,
        handler,
        priority: options.priority,
        source: installedPluginEntry,
      });
    },
  });
  assert.ok(typedHooks.some((hook) => hook.hookName === "before_tool_call"));
  assert.ok(typedHooks.some((hook) => hook.hookName === "after_tool_call"));

  const activeAssignmentDir = path.join(stateDir, "teams", "real-hook-host", "leader");
  await fs.mkdir(activeAssignmentDir, { recursive: true });
  await fs.writeFile(path.join(activeAssignmentDir, "active-assignment.json"), JSON.stringify({
    teamId: "real-hook-host",
    memberId: "leader",
    role: "leader",
    taskId: "team-75-task-101",
    rootTaskId: "team-75-task-101",
    messageId: "real-hook-host-message",
    rootMessageId: "real-hook-host-message",
    assignmentId: "leader-real-hook-host",
    workId: "leader-real-hook-host",
    revision: 1,
    activeAssignmentContext: {
      teamId: "real-hook-host",
      memberId: "leader",
      recordedAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      terminal: false,
    },
  }), "utf8");

  hookRunnerGlobal.i({
    hooks: [],
    typedHooks,
    plugins: [{ id: "redis-team", status: "loaded" }],
  });
  const hookRunner = hookRunnerGlobal.t();
  assert.ok(hookRunner?.hasHooks("before_tool_call"), "real OpenClaw hook runner sees redis-team");

  const toolNames = [
    "read",
    "exec",
    "team_artifact_write",
    "team_artifact_read",
    "team_artifact_list",
    "team_send",
    "team_update_progress",
    "team_complete_task",
    "browser",
  ];
  for (const [index, toolName] of toolNames.entries()) {
    const params = toolName === "exec"
      ? { command: "pwd" }
      : toolName === "browser"
        ? { action: "status" }
        : {};
    const ctx = {
      agentId: "main",
      sessionKey: `agent:main:redis-team-hook-host:${index}`,
      config,
      loopDetection: { enabled: false },
    };
    const outcome = await agentHarness.runBeforeToolCallHook({
      toolName,
      params,
      toolCallId: `hook-host-${index}`,
      ctx,
    });
    assert.equal(outcome.blocked, false, `${toolName} must not be blocked by a hook failure`);
    await hookRunner.runAfterToolCall(
      { toolName, params, result: { ok: true }, durationMs: 1 },
      { agentId: "main", sessionKey: ctx.sessionKey },
    );
  }

  const artifactTool = tools.get("team_artifact_write");
  assert.ok(artifactTool, "redis-team registered team_artifact_write in the real SDK host");
  const artifactResult = await artifactTool.execute("real-host-artifact", {
    rootTaskId: "team-75-task-101",
    scope: "team",
    kind: "plan",
    path: "hook-host-plan.md",
    content: "# Real OpenClaw hook host validation\n",
  });
  const artifactPayload = JSON.parse(artifactResult.content[0].text);
  assert.equal(artifactPayload.artifact.path, "/team/results/team-75-task-101/plan/hook-host-plan.md");
  assert.equal(
    await fs.readFile(path.join(sharedDir, "results", "team-75-task-101", "plan", "hook-host-plan.md"), "utf8"),
    "# Real OpenClaw hook host validation\n",
  );

  console.log("redis-team real OpenClaw hook host test passed");
} finally {
  await fs.rm(root, { recursive: true, force: true });
}
