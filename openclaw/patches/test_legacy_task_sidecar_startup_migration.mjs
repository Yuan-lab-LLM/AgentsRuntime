import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const patchScript = path.resolve(import.meta.dirname, "patch_legacy_task_sidecar_startup_migration.mjs");
const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-task-sidecar-migration-patch-"));

try {
  const distRoot = path.join(fixtureRoot, "dist");
  fs.mkdirSync(distRoot, { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, "package.json"), JSON.stringify({ version: "2026.7.1-2", type: "module" }));
  const fixturePath = path.join(distRoot, "state-migrations-fixture.js");
  fs.writeFileSync(fixturePath, [
    "async function migrateLegacyTaskRunsSidecar(params) { return params.taskRuns; }",
    "async function migrateLegacyFlowRunsSidecar(params) { return params.flowRuns; }",
    "async function migrateLegacyTaskStateSidecars(params) {",
    "\tconst taskRuns = await migrateLegacyTaskRunsSidecar(params);",
    "\tconst flowRuns = await migrateLegacyFlowRunsSidecar(params);",
    "\treturn {",
    "\t\tchanges: [...taskRuns.changes, ...flowRuns.changes],",
    "\t\twarnings: [...taskRuns.warnings, ...flowRuns.warnings]",
    "\t};",
    "}",
    "export { migrateLegacyTaskStateSidecars };",
  ].join("\n"));

  const run = (mode) => spawnSync(process.execPath, [patchScript, mode], {
    env: { ...process.env, OPENCLAW_PACKAGE_ROOT: fixtureRoot },
    encoding: "utf8",
  });
  const patched = run("--patch");
  assert.equal(patched.status, 0, patched.stderr || patched.stdout);
  const verified = run("--verify");
  assert.equal(verified.status, 0, verified.stderr || verified.stdout);

  const module = await import(`${pathToFileURL(fixturePath).href}?patched=1`);
  const result = await module.migrateLegacyTaskStateSidecars({
    taskRuns: {
      changes: [],
      warnings: [
        "Failed reading task registry sidecar /state/tasks/runs.sqlite: Error: database disk image is malformed",
        "Left task registry sidecar in place because 1 row already existed in shared state: task-1",
        "Failed migrating task registry sidecar /state/tasks/runs.sqlite: Error: shared state is read-only",
      ],
    },
    flowRuns: { changes: [], warnings: [] },
  });
  assert.equal(result.notices.length, 2, "optional legacy read/conflict results must be notices");
  assert.equal(result.warnings.length, 1, "a real shared-state migration failure must remain blocking");
  assert.match(result.warnings[0], /shared state is read-only/);
  assert.equal(result.warnings.length === 0, false, "real migration failures must not record the startup checkpoint");

  const optionalOnly = await module.migrateLegacyTaskStateSidecars({
    taskRuns: {
      changes: [],
      warnings: ["Failed reading task registry sidecar /state/tasks/runs.sqlite: Error: database disk image is malformed"],
    },
    flowRuns: { changes: [], warnings: [] },
  });
  assert.deepEqual(optionalOnly.warnings, []);
  assert.equal(optionalOnly.notices.length, 1);
  assert.equal(optionalOnly.warnings.length === 0, true, "optional task history must allow the startup checkpoint");
} finally {
  fs.rmSync(fixtureRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw optional legacy task sidecar startup migration patch test passed\n");
