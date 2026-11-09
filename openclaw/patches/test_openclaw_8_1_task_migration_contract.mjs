import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
assert.equal(packageJson.version, "2026.8.1", "task migration contract is locked to OpenClaw 2026.8.1");

const distDir = path.join(packageRoot, "dist");
const candidates = fs.readdirSync(distDir)
  .filter((name) => /^state-migrations(?:\.|-).*\.js$/.test(name))
  .map((name) => path.join(distDir, name))
  .filter((file) => fs.readFileSync(file, "utf8").includes('path.join(stateDir, "tasks", "runs.sqlite")'));
assert.equal(candidates.length, 1, `expected one 8.1 state migration bundle, found ${candidates.length}`);
const source = fs.readFileSync(candidates[0], "utf8");
for (const contract of [
  "runOpenClawStateWriteTransaction",
  "legacyRowsMatch(existing, row, taskColumns)",
  "throw new LegacyTaskStateSidecarConflictError(conflicts)",
  "Left task registry sidecar in place because",
  "firstFreeArchivePath(sourcePath)",
  "fs.readFileSync(params.sourcePath).equals(fs.readFileSync(archivedPath))",
]) {
  assert.ok(source.includes(contract), `OpenClaw 8.1 task migration contract missing: ${contract}`);
}
assert.ok(!source.includes("CLAWMANAGER_OPTIONAL_LEGACY_TASK_SIDECAR_MIGRATION"));
process.stdout.write("OpenClaw 8.1 upstream transactional task migration contract passed\n");
