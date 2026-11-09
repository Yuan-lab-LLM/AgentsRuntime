import fs from "node:fs";
import path from "node:path";

const expectedVersion = "2026.8.1";
const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || "/usr/local/lib/node_modules/openclaw";
const packageJson = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (packageJson.version !== expectedVersion) {
  throw new Error(`refusing to patch OpenClaw ${packageJson.version}; expected ${expectedVersion}`);
}

const distRoot = path.join(packageRoot, "dist");
const targets = fs.readdirSync(distRoot)
  .filter((name) => /^exec-approvals-generated-migration-.*\.js$/.test(name))
  .map((name) => path.join(distRoot, name));
if (targets.length !== 1) {
  throw new Error(`expected one generated exec approvals module, found ${targets.length}`);
}

const target = targets[0];
const patching = process.argv.includes("--patch");
const patchMarker = "CLAWMANAGER_DOCTOR_EXEC_APPROVALS_MIGRATION_AUTHORITY";
const anchor = [
  "function pathMayExist(filePath) {",
  "\ttry {",
].join("\n");
const helper = [
  `// ${patchMarker}: Doctor must be able to reach its own one-time migration.`,
  "function doctorOwnsExecApprovalsMigration() {",
  "\tif (process.env.CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION !== \"1\") return false;",
  "\tconst args = process.argv.slice(2);",
  "\tconst doctor = args.indexOf(\"doctor\");",
  "\tif (doctor < 0) return false;",
  "\tconst doctorArgs = args.slice(doctor + 1);",
  "\treturn doctorArgs.includes(\"--non-interactive\") && (doctorArgs.includes(\"--fix\") || doctorArgs.includes(\"--repair\"));",
  "}",
  anchor,
].join("\n");
const gateAnchor = [
  "function assertNoPendingLegacyExecApprovals(options = {}) {",
  "\tconst sourcePath = resolveExecApprovalsPath();",
].join("\n");
const patchedGate = [
  "function assertNoPendingLegacyExecApprovals(options = {}) {",
  "\tif (doctorOwnsExecApprovalsMigration()) return;",
  "\tconst sourcePath = resolveExecApprovalsPath();",
].join("\n");

function replaceOnce(source, needle, replacement, label) {
  const occurrences = source.split(needle).length - 1;
  if (occurrences !== 1) throw new Error(`expected one ${label}, found ${occurrences}`);
  return source.replace(needle, replacement);
}

function verify(source) {
  for (const required of [
    patchMarker,
    'process.env.CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION !== "1"',
    'doctorArgs.includes("--non-interactive")',
    'doctorArgs.includes("--fix") || doctorArgs.includes("--repair")',
    "if (doctorOwnsExecApprovalsMigration()) return;",
  ]) {
    if (!source.includes(required)) throw new Error(`exec approvals Doctor patch is incomplete: ${required}`);
  }
}

let source = fs.readFileSync(target, "utf8");
if (patching && !source.includes(patchMarker)) {
  source = replaceOnce(source, anchor, helper, "path probe anchor");
  source = replaceOnce(source, gateAnchor, patchedGate, "legacy migration gate");
  fs.writeFileSync(target, source);
}

source = fs.readFileSync(target, "utf8");
verify(source);
process.stdout.write(`OpenClaw Doctor exec approvals migration patch verified in ${target}\n`);
