import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.7.1-2";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (packageJson.version !== expectedVersion) {
  throw new Error(`refusing to patch OpenClaw ${packageJson.version}; expected ${expectedVersion}`);
}

const distRoot = path.join(packageRoot, "dist");
const functionStart = "async function migrateLegacyTaskStateSidecars(params) {";
const candidates = fs.readdirSync(distRoot)
  .filter((name) => name.startsWith("state-migrations-") && name.endsWith(".js"))
  .map((name) => path.join(distRoot, name))
  .filter((filePath) => fs.readFileSync(filePath, "utf8").includes(functionStart));
if (candidates.length !== 1) {
  throw new Error(`expected one OpenClaw state migration bundle, found ${candidates.length}`);
}

const target = candidates[0];
const patching = process.argv.includes("--patch");
const patchMarker = "CLAWMANAGER_OPTIONAL_LEGACY_TASK_SIDECARS";
const originalFunction = [
  "async function migrateLegacyTaskStateSidecars(params) {",
  "\tconst taskRuns = await migrateLegacyTaskRunsSidecar(params);",
  "\tconst flowRuns = await migrateLegacyFlowRunsSidecar(params);",
  "\treturn {",
  "\t\tchanges: [...taskRuns.changes, ...flowRuns.changes],",
  "\t\twarnings: [...taskRuns.warnings, ...flowRuns.warnings]",
  "\t};",
  "}",
].join("\n");
const patchedFunction = [
  "async function migrateLegacyTaskStateSidecars(params) {",
  "\tconst taskRuns = await migrateLegacyTaskRunsSidecar(params);",
  "\tconst flowRuns = await migrateLegacyFlowRunsSidecar(params);",
  `\t// ${patchMarker}: optional 5.4 task history must not prevent the 7.1 gateway from starting.`,
  "\tconst compatibilityNoticePrefixes = [",
  "\t\t\"Failed reading task registry sidecar \",",
  "\t\t\"Failed reading task flow sidecar \",",
  "\t\t\"Left task registry sidecar in place because \",",
  "\t\t\"Left task flow sidecar in place because \"",
  "\t];",
  "\tconst sidecarWarnings = [...taskRuns.warnings, ...flowRuns.warnings];",
  "\tconst notices = sidecarWarnings.filter((warning) => compatibilityNoticePrefixes.some((prefix) => warning.startsWith(prefix)));",
  "\tconst warnings = sidecarWarnings.filter((warning) => !compatibilityNoticePrefixes.some((prefix) => warning.startsWith(prefix)));",
  "\treturn {",
  "\t\tchanges: [...taskRuns.changes, ...flowRuns.changes],",
  "\t\twarnings,",
  "\t\t...notices.length > 0 ? { notices } : {}",
  "\t};",
  "}",
].join("\n");

function verify(source) {
  if (!source.includes(patchMarker)) {
    throw new Error("legacy task sidecar startup migration patch marker is missing");
  }
  for (const required of [
    "Failed reading task registry sidecar ",
    "Left task registry sidecar in place because ",
    "const warnings = sidecarWarnings.filter",
    "...notices.length > 0 ? { notices } : {}",
  ]) {
    if (!source.includes(required)) throw new Error(`legacy task sidecar patch is incomplete: ${required}`);
  }
  if (source.includes(originalFunction)) {
    throw new Error("unpatched legacy task sidecar migration function remains");
  }
}

let source = fs.readFileSync(target, "utf8");
if (patching && !source.includes(patchMarker)) {
  const occurrences = source.split(originalFunction).length - 1;
  if (occurrences !== 1) {
    throw new Error(`expected one legacy task sidecar migration function, found ${occurrences}`);
  }
  source = source.replace(originalFunction, patchedFunction);
  fs.writeFileSync(target, source);
}

source = fs.readFileSync(target, "utf8");
verify(source);
process.stdout.write(`OpenClaw optional legacy task sidecar startup migration patch verified in ${target}\n`);
