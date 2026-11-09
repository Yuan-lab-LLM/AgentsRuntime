import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.8.1";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (![expectedVersion, "2026.10.2"].includes(packageJson.version)) {
  throw new Error(`refusing to patch OpenClaw ${packageJson.version}; expected ${expectedVersion}`);
}

const target = path.join(packageRoot, "dist", "extensions", "memory-core", "doctor-contract-api.js");
const patching = process.argv.includes("--patch");
const patchMarker = "CLAWMANAGER_SAFE_IDEMPOTENT_MEMORY_ARCHIVE";
const readFileBinding = packageJson.version === "2026.10.2" ? "fs" : "fs$1";
const migrationStart = 'id: "memory-core-legacy-sidecar-index-to-agent-sqlite",';
const declarations = [
  "\tasync migrateLegacyState(params) {",
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
].join("\n");
const patchedDeclarations = [
  "\tasync migrateLegacyState(params) {",
  "\t\tconst changes = [];",
  "\t\tconst warnings = [];",
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
const archiveCollision = [
  "\tif (existingArchives.length > 0) {",
  "\t\tparams.warnings.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);",
  "\t\treturn;",
  "\t}",
].join("\n");
const patchedArchiveCollision = [
  "\tif (existingArchives.length > 0) {",
  `\t\t// ${patchMarker}: only byte-identical source/archive pairs are idempotent.`,
  "\t\tconst allArchivesMatch = await Promise.all(existingSources.map(async (sourcePath) => {",
  "\t\t\tconst archivedPath = `${sourcePath}.migrated`;",
  "\t\t\tif (!await legacyStateFileExists(archivedPath)) return false;",
  `\t\t\tconst [sourceBytes, archivedBytes] = await Promise.all([${readFileBinding}.readFile(sourcePath), ${readFileBinding}.readFile(archivedPath)]);`,
  "\t\t\treturn sourceBytes.equals(archivedBytes);",
  "\t\t})).then((matches) => matches.every(Boolean)).catch(() => false);",
  "\t\tif (allArchivesMatch) {",
  "\t\t\tparams.notices.push(`Left byte-identical migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists`);",
  "\t\t\treturn;",
  "\t\t}",
  "\t\tparams.warnings.push(`Left migrated Memory Core legacy memory index sidecar in place because ${existingArchives[0]} already exists and differs from the source`);",
  "\t\treturn;",
  "\t}",
].join("\n");

function replaceOnce(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== 1) throw new Error(`expected one ${label}, found ${occurrences}`);
  return source.replace(needle, replacement);
}

function verify(source) {
  const migrationStartIndex = source.indexOf(migrationStart);
  if (migrationStartIndex < 0) throw new Error("Memory Core legacy index migration is missing");
  const migration = source.slice(migrationStartIndex);
  for (const required of [patchMarker, "const allArchivesMatch = await Promise.all", "sourceBytes.equals(archivedBytes)", "already exists and differs from the source", "const notices = [];"]) {
    if (!source.includes(required)) throw new Error(`Memory Core archive patch is incomplete: ${required}`);
  }
  if (!migration.includes(patchedArchiveCall) || !migration.includes(patchedMigrationReturn)) {
    throw new Error("Memory Core archive notices are not propagated by the migration");
  }
  if (source.includes("CLAWMANAGER_IDEMPOTENT_MEMORY_CORE_MIGRATION")) {
    throw new Error("the retired Dreams import patch must not be present");
  }
}

let source = fs.readFileSync(target, "utf8");
if (patching && !source.includes(patchMarker)) {
  const migrationStartIndex = source.indexOf(migrationStart);
  if (migrationStartIndex < 0) throw new Error("could not isolate the Memory Core legacy index migration");
  const prefix = source.slice(0, migrationStartIndex);
  let migration = source.slice(migrationStartIndex);
  migration = replaceOnce(migration, declarations, patchedDeclarations, "Memory Core legacy index declarations");
  migration = replaceOnce(migration, archiveCall, patchedArchiveCall, "Memory Core legacy index archive call");
  migration = replaceOnce(migration, migrationReturn, patchedMigrationReturn, "Memory Core legacy index return");
  source = replaceOnce(prefix + migration, archiveCollision, patchedArchiveCollision, "Memory Core archive collision branch");
  fs.writeFileSync(target, source);
}

source = fs.readFileSync(target, "utf8");
verify(source);
process.stdout.write(`OpenClaw safe idempotent Memory Core archive patch verified in ${target}\n`);
