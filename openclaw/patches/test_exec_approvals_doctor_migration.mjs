import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const root = fs.mkdtempSync(path.join(os.tmpdir(), "openclaw-exec-approvals-doctor-"));
const home = path.join(root, "home");
const stateDir = path.join(home, ".openclaw");
const configPath = path.join(stateDir, "openclaw.json");
const legacyPath = path.join(stateDir, "exec-approvals.json");

function command(args, extraEnv = {}) {
  return spawnSync("openclaw", args, {
    cwd: root,
    encoding: "utf8",
    timeout: 120_000,
    env: {
      ...process.env,
      HOME: home,
      OPENCLAW_STATE_DIR: stateDir,
      OPENCLAW_CONFIG_PATH: configPath,
      NO_COLOR: "1",
      ...extraEnv,
    },
  });
}

try {
  fs.mkdirSync(stateDir, { recursive: true, mode: 0o700 });
  fs.writeFileSync(configPath, "{}\n", { mode: 0o600 });
  const legacy = {
    version: 1,
    socket: { path: path.join(root, "exec.sock"), token: "fixture-token" },
    defaults: {},
    agents: {},
  };
  fs.writeFileSync(legacyPath, `${JSON.stringify(legacy)}\n`, { mode: 0o600 });

  // Exercise the same generated store used by Gateway host-execution paths.
  // `config validate` intentionally does not load this store and therefore is
  // not a meaningful fail-closed assertion.
  process.env.HOME = home;
  process.env.OPENCLAW_STATE_DIR = stateDir;
  process.env.OPENCLAW_CONFIG_PATH = configPath;
  process.env.CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION = "1";
  const distRoot = "/usr/local/lib/node_modules/openclaw/dist";
  const version = JSON.parse(fs.readFileSync("/usr/local/lib/node_modules/openclaw/package.json")).version;
  const migrationModules = fs.readdirSync(distRoot)
    .filter((name) => version === '2026.10.2'
      ? /^exec-approvals-migration-gate-.*\.mjs$/.test(name) && fs.readFileSync(path.join(distRoot, name), 'utf8').includes('function assertNoPendingLegacyExecApprovals(')
      : /^exec-approvals-generated-migration-.*\.js$/.test(name));
  assert.equal(migrationModules.length, 1);
  const execApprovalsStore = await import(pathToFileURL(path.join(distRoot, migrationModules[0])).href);
  const checkStore = version === '2026.10.2' ? execApprovalsStore.n : execApprovalsStore.a;
  assert.throws(
    () => checkStore(),
    /Legacy exec approvals exist/,
    "ordinary store access must remain fail-closed even when the authority environment variable is present",
  );

  const repaired = command(["doctor", "--fix", "--yes", "--non-interactive"], {
    CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION: "1",
  });
  assert.equal(repaired.status, 0, repaired.stderr || repaired.stdout);
  assert.equal(fs.existsSync(legacyPath), false, "Doctor must retire the verified legacy JSON");
  assert.equal(fs.existsSync(`${legacyPath}.doctor-importing`), false, "Doctor claim must not remain");
  assert.doesNotThrow(() => checkStore(), "migrated exec approvals must be readable from SQLite");

  const validated = command(["config", "validate", "--json"]);
  assert.equal(validated.status, 0, validated.stderr || validated.stdout);
} finally {
  fs.rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}

process.stdout.write("OpenClaw Doctor exec approvals migration integration test passed\n");
