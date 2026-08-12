import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const patchScript = path.resolve(import.meta.dirname, "patch_memory_core_startup_migration.mjs");
const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-memory-core-migration-patch-"));

try {
  const targetDir = path.join(fixtureRoot, "dist", "extensions", "memory-core");
  fs.mkdirSync(targetDir, { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, "package.json"), JSON.stringify({ version: "2026.7.1-2", type: "module" }));
  const fixturePath = path.join(targetDir, "doctor-contract-api.js");
  fs.writeFileSync(fixturePath, [
    "function configureMemoryCoreDreamingState() {}",
    "async function collectLegacySources(_config, env) {",
    "\treturn [{ label: env.FAIL_IMPORT === '1' ? 'broken import' : 'session ingestion', workspaceDir: '/workspace', fail: env.FAIL_IMPORT === '1' }];",
    "}",
    "function targetNamespacesForSource() { return ['memory-core']; }",
    "async function workspaceHasRows(_namespace, _workspaceDir) { return process.env.TARGET_HAS_ROWS === '1'; }",
    "async function migrateSource(source) { if (source.fail) throw new Error('broken legacy JSON'); return 1; }",
    "async function archiveLegacyStateSource() {}",
    "async function archiveLegacyMemorySidecar(params) {",
    "\tconst existingArchives = ['/state/memory/main.sqlite.migrated'];",
    "\tif (process.env.ARCHIVE_EXISTS === '1') {",
    "\t\tparams.warnings.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);",
    "\t\treturn;",
    "\t}",
    "}",
    "async function collectLegacyMemorySidecarSources() { return [{ agentId: 'main' }]; }",
    "function groupLegacyMemorySidecarSourcesByPath(sources) { return [sources]; }",
    "async function migrateLegacyMemorySidecarSource() { return { archiveReady: true }; }",
    "async function preserveLegacyMemorySidecarRetryPath() {}",
    "const stateMigrations = [{",
    "\tid: \"memory-core-dreams-json-to-sqlite\",",
    "\tlabel: \"Memory Core dreaming state\",",
    "\tasync detectLegacyState() { return null; },",
    "\tasync migrateLegacyState(params) {",
    "\t\tconfigureMemoryCoreDreamingState(params.context.openPluginStateKeyedStore);",
    "\t\tconst changes = [];",
    "\t\tconst warnings = [];",
    "\t\tfor (const source of await collectLegacySources(params.config, params.env)) {",
    "\t\t\tif ((await Promise.all(targetNamespacesForSource(source.label).map((namespace) => workspaceHasRows(namespace, source.workspaceDir)))).some(Boolean)) {",
    "\t\t\t\twarnings.push(`Skipped Memory Core ${source.label} import for ${source.workspaceDir} because SQLite rows already exist; left legacy source in place`);",
    "\t\t\t\tcontinue;",
    "\t\t\t}",
    "\t\t\tlet imported;",
    "\t\t\ttry {",
    "\t\t\t\timported = await migrateSource(source);",
    "\t\t\t} catch (err) {",
    "\t\t\t\twarnings.push(`Skipped Memory Core ${source.label} import for ${source.workspaceDir} because the legacy source could not be imported: ${String(err)}`);",
    "\t\t\t\tcontinue;",
    "\t\t\t}",
    "\t\t\tchanges.push(`Migrated Memory Core ${source.label} -> SQLite plugin state (${imported} row(s))`);",
    "\t\t\tawait archiveLegacyStateSource({ filePath: source.filePath, label: source.label, changes, warnings });",
    "\t\t}",
    "\t\treturn {",
    "\t\t\tchanges,",
    "\t\t\twarnings",
    "\t\t};",
    "\t}",
    "}, {",
    "\tid: \"memory-core-legacy-sidecar-index-to-agent-sqlite\",",
    "\tlabel: \"Memory Core legacy memory index sidecar\",",
    "\tasync migrateLegacyState(params) {",
    "\t\tconst changes = [];",
    "\t\tconst warnings = [];",
    "\t\tconst groups = groupLegacyMemorySidecarSourcesByPath(await collectLegacyMemorySidecarSources());",
    "\t\tfor (const sources of groups) {",
    "\t\t\tlet archiveReady = true;",
    "\t\t\tfor (const source of sources) try {",
    "\t\t\t\tconst result = await migrateLegacyMemorySidecarSource({ source, changes, warnings });",
    "\t\t\t\tarchiveReady &&= result.archiveReady;",
    "\t\t\t} catch (err) {",
    "\t\t\t\tarchiveReady = false;",
    "\t\t\t\tawait preserveLegacyMemorySidecarRetryPath({ source, changes, warnings });",
    "\t\t\t\twarnings.push(`Skipped Memory Core legacy memory index import for agent ${source.agentId} because the sidecar could not be imported: ${String(err)}`);",
    "\t\t\t}",
    "\t\t\tif (archiveReady && sources[0]) await archiveLegacyMemorySidecar({",
    "\t\t\t\tsource: sources[0],",
    "\t\t\t\tchanges,",
    "\t\t\t\twarnings",
    "\t\t\t});",
    "\t\t}",
    "\t\treturn {",
    "\t\t\tchanges,",
    "\t\t\twarnings",
    "\t\t};",
    "\t}",
    "}];",
    "export { stateMigrations };",
  ].join("\n"));

  const run = (mode) => spawnSync(process.execPath, [patchScript, mode], {
    env: { ...process.env, OPENCLAW_PACKAGE_ROOT: fixtureRoot },
    encoding: "utf8",
  });
  const patched = run("--patch");
  assert.equal(patched.status, 0, patched.stderr || patched.stdout);
  const verified = run("--verify");
  assert.equal(verified.status, 0, verified.stderr || verified.stdout);

  process.env.TARGET_HAS_ROWS = "1";
  const module = await import(`${pathToFileURL(fixturePath).href}?patched=1`);
  const migration = module.stateMigrations[0];
  const converged = await migration.migrateLegacyState({
    config: {},
    env: {},
    context: { openPluginStateKeyedStore() {} },
  });
  assert.deepEqual(converged.changes, []);
  assert.deepEqual(converged.warnings, [], "already-imported SQLite rows must not block startup");
  assert.equal(converged.notices.length, 1);
  assert.match(converged.notices[0], /SQLite rows already exist/);
  const checkpointRecorded = converged.warnings.length === 0;
  assert.equal(checkpointRecorded, true, "idempotent convergence must allow the startup checkpoint");

  process.env.TARGET_HAS_ROWS = "0";
  const failedImport = await migration.migrateLegacyState({
    config: {},
    env: { FAIL_IMPORT: "1" },
    context: { openPluginStateKeyedStore() {} },
  });
  assert.equal(failedImport.warnings.length, 1, "real import failures must remain startup-blocking warnings");
  assert.match(failedImport.warnings[0], /legacy source could not be imported/);

  process.env.ARCHIVE_EXISTS = "1";
  const archiveCollision = await module.stateMigrations[1].migrateLegacyState({ config: {}, env: {} });
  assert.deepEqual(archiveCollision.warnings, [], "an existing .migrated archive must not block startup");
  assert.equal(archiveCollision.notices.length, 1);
  assert.match(archiveCollision.notices[0], /already exists/);
  assert.equal(archiveCollision.warnings.length === 0, true, "an idempotent archive collision must allow the startup checkpoint");
} finally {
  delete process.env.TARGET_HAS_ROWS;
  delete process.env.ARCHIVE_EXISTS;
  fs.rmSync(fixtureRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw idempotent Memory Core startup migration patch test passed\n");
