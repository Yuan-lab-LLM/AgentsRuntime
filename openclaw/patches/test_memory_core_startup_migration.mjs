import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const patchScript = path.resolve(import.meta.dirname, "patch_memory_core_startup_migration.mjs");
const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-memory-archive-patch-"));

try {
  const targetDir = path.join(fixtureRoot, "dist", "extensions", "memory-core");
  fs.mkdirSync(targetDir, { recursive: true });
  fs.writeFileSync(path.join(fixtureRoot, "package.json"), JSON.stringify({ version: process.env.TEST_OPENCLAW_CORE_VERSION || "2026.8.1", type: "module" }));
  const fixturePath = path.join(targetDir, "doctor-contract-api.js");
  fs.writeFileSync(fixturePath, [
    "const fs$1 = { readFile: async (file) => Buffer.from(process.env.ARCHIVE_DIFFERS === '1' && file.endsWith('.migrated') ? 'archive' : 'source') };",
    "const fs = fs$1;",
    "async function legacyStateFileExists() { return true; }",
    "async function archiveLegacyMemorySidecar(params) {",
    "\tconst existingSources = [params.source.legacyPath];",
    "\tif (existingSources.length === 0) return;",
    "\tconst existingArchives = [`${params.source.legacyPath}.migrated`];",
    "\tif (existingArchives.length > 0) {",
    "\t\tparams.warnings.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);",
    "\t\treturn;",
    "\t}",
    "}",
    "async function collectLegacyMemorySidecarSources() { return [{ agentId: 'main', legacyPath: '/state/main.sqlite' }]; }",
    "function groupLegacyMemorySidecarSourcesByPath(sources) { return [sources]; }",
    "async function migrateLegacyMemorySidecarSource() { if (process.env.FAIL_IMPORT === '1') throw new Error('broken sqlite'); return { archiveReady: true }; }",
    "async function preserveLegacyMemorySidecarRetryPath() {}",
    "const stateMigrations = [{",
    "\tid: \"memory-core-legacy-sidecar-index-to-agent-sqlite\",",
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

  const run = (mode) => spawnSync(process.execPath, [patchScript, mode], { env: { ...process.env, OPENCLAW_PACKAGE_ROOT: fixtureRoot }, encoding: "utf8" });
  const patched = run("--patch");
  assert.equal(patched.status, 0, patched.stderr || patched.stdout);
  const verified = run("--verify");
  assert.equal(verified.status, 0, verified.stderr || verified.stdout);

  const module = await import(`${pathToFileURL(fixturePath).href}?patched=1`);
  const migration = module.stateMigrations[0];
  delete process.env.ARCHIVE_DIFFERS;
  const idempotent = await migration.migrateLegacyState({});
  assert.deepEqual(idempotent.warnings, []);
  assert.equal(idempotent.notices.length, 1);
  assert.match(idempotent.notices[0], /byte-identical/);

  process.env.ARCHIVE_DIFFERS = "1";
  const conflict = await migration.migrateLegacyState({});
  assert.equal(conflict.notices.length, 0);
  assert.equal(conflict.warnings.length, 1, "different source/archive content must remain blocking");
  assert.match(conflict.warnings[0], /differs from the source/);

  process.env.FAIL_IMPORT = "1";
  const failedImport = await migration.migrateLegacyState({});
  assert.equal(failedImport.warnings.length, 1, "real import failures must remain blocking");
  assert.match(failedImport.warnings[0], /sidecar could not be imported/);
} finally {
  delete process.env.ARCHIVE_DIFFERS;
  delete process.env.FAIL_IMPORT;
  fs.rmSync(fixtureRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw safe idempotent Memory Core archive patch test passed\n");
