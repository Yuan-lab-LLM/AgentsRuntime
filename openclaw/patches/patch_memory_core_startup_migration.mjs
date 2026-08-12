import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.7.1-2";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (packageJson.version !== expectedVersion) {
  throw new Error(`refusing to patch OpenClaw ${packageJson.version}; expected ${expectedVersion}`);
}

const target = path.join(packageRoot, "dist", "extensions", "memory-core", "doctor-contract-api.js");
const patching = process.argv.includes("--patch");
const patchMarker = "CLAWMANAGER_IDEMPOTENT_MEMORY_CORE_MIGRATION";
const archivePatchMarker = "CLAWMANAGER_IDEMPOTENT_MEMORY_CORE_ARCHIVE";
const migrationStart = 'id: "memory-core-dreams-json-to-sqlite",';
const migrationEnd = 'id: "memory-core-legacy-sidecar-index-to-agent-sqlite",';
const declarations = [
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
].join("\n");
const patchedDeclarations = [
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
  `\t\t// ${patchMarker}: an existing target row means the import already converged.`,
  "\t\tconst notices = [];",
].join("\n");
const existingRowsWarning = "warnings.push(`Skipped Memory Core ${source.label} import for ${source.workspaceDir} because SQLite rows already exist; left legacy source in place`);";
const existingRowsNotice = "notices.push(`Skipped Memory Core ${source.label} import for ${source.workspaceDir} because SQLite rows already exist; left legacy source in place`);";
const migrationReturn = [
  "\t\treturn {",
  "\t\t\tchanges,",
  "\t\t\twarnings",
  "\t\t};",
].join("\n");
const patchedMigrationReturn = [
  "\t\treturn {",
  "\t\t\tchanges,",
  "\t\t\twarnings,",
  "\t\t\tnotices",
  "\t\t};",
].join("\n");
const archiveExistingWarning = "params.warnings.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);";
const archiveExistingNotice = "params.notices.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);";
const legacyIndexDeclarations = [
  "\tasync migrateLegacyState(params) {",
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
].join("\n");
const patchedLegacyIndexDeclarations = [
  "\tasync migrateLegacyState(params) {",
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
  `\t\t// ${archivePatchMarker}: a prior archive is an idempotent completion signal.`,
  "\t\tconst notices = [];",
].join("\n");
const archiveCall = [
  "\t\t\tif (archiveReady && sources[0]) await archiveLegacyMemorySidecar({",
  "\t\t\t\tsource: sources[0],",
  "\t\t\t\tchanges,",
  "\t\t\t\twarnings",
  "\t\t\t});",
].join("\n");
const patchedArchiveCall = [
  "\t\t\tif (archiveReady && sources[0]) await archiveLegacyMemorySidecar({",
  "\t\t\t\tsource: sources[0],",
  "\t\t\t\tchanges,",
  "\t\t\t\twarnings,",
  "\t\t\t\tnotices",
  "\t\t\t});",
].join("\n");

function replaceOnce(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== 1) throw new Error(`expected one ${label}, found ${occurrences}`);
  return source.replace(needle, replacement);
}

function migrationSlice(source) {
  const start = source.indexOf(migrationStart);
  const end = source.indexOf(migrationEnd, start + migrationStart.length);
  if (start < 0 || end < 0 || end <= start) {
    throw new Error("could not isolate the Memory Core dreams migration");
  }
  return { start, end, source: source.slice(start, end) };
}

function verify(source) {
  const migration = migrationSlice(source).source;
  for (const required of [patchMarker, "const notices = [];", existingRowsNotice, patchedMigrationReturn]) {
    if (!migration.includes(required)) throw new Error(`Memory Core startup migration patch is incomplete: ${required}`);
  }
  if (migration.includes(existingRowsWarning)) {
    throw new Error("existing target rows are still classified as a startup-blocking warning");
  }
  for (const required of [archivePatchMarker, archiveExistingNotice, patchedLegacyIndexDeclarations, patchedArchiveCall]) {
    if (!source.includes(required)) throw new Error(`Memory Core archive migration patch is incomplete: ${required}`);
  }
  if (source.includes(archiveExistingWarning)) {
    throw new Error("an existing Memory Core archive is still classified as a startup-blocking warning");
  }
}

let source = fs.readFileSync(target, "utf8");
if (patching && !source.includes(patchMarker)) {
  const migration = migrationSlice(source);
  let patchedMigration = migration.source;
  patchedMigration = replaceOnce(patchedMigration, declarations, patchedDeclarations, "Memory Core migration result declarations");
  patchedMigration = replaceOnce(patchedMigration, existingRowsWarning, existingRowsNotice, "idempotent Memory Core import result");
  patchedMigration = replaceOnce(patchedMigration, migrationReturn, patchedMigrationReturn, "Memory Core migration result return");
  source = source.slice(0, migration.start) + patchedMigration + source.slice(migration.end);
}
if (patching && !source.includes(archivePatchMarker)) {
  const legacyIndexStart = source.indexOf(migrationEnd);
  if (legacyIndexStart < 0) throw new Error("could not isolate the Memory Core legacy index migration");
  const prefix = source.slice(0, legacyIndexStart);
  let legacyIndexMigration = source.slice(legacyIndexStart);
  legacyIndexMigration = replaceOnce(legacyIndexMigration, legacyIndexDeclarations, patchedLegacyIndexDeclarations, "Memory Core legacy index declarations");
  legacyIndexMigration = replaceOnce(legacyIndexMigration, archiveCall, patchedArchiveCall, "Memory Core archive call");
  legacyIndexMigration = replaceOnce(legacyIndexMigration, migrationReturn, patchedMigrationReturn, "Memory Core legacy index result return");
  source = prefix + legacyIndexMigration;
  source = replaceOnce(source, archiveExistingWarning, archiveExistingNotice, "idempotent Memory Core archive result");
}
if (patching) {
  fs.writeFileSync(target, source);
}

source = fs.readFileSync(target, "utf8");
verify(source);
process.stdout.write(`OpenClaw idempotent Memory Core startup migration patch verified in ${target}\n`);
