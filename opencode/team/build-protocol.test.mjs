import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { buildProtocol } from "./build-protocol.mjs";

test("the delivery package imports the authoritative protocol without an OpenClaw SDK", async () => {
 const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "opencode-team-protocol-"));
 try {
  const output = path.join(temporary, "protocol.mjs");
  await buildProtocol(output);
  const protocol = await import(pathToFileURL(output));
  assert.equal(typeof protocol.createRuntime, "function");
  assert.equal(typeof protocol.startConsumer, "function");
  assert.equal(typeof protocol.RedisClient, "function");
  const original = process.env.CLAWMANAGER_RUNTIME_TYPE;
  try {
   delete process.env.CLAWMANAGER_RUNTIME_TYPE;
   let cfg = protocol.readChannelConfig({});
   assert.equal(protocol.taskEvent(cfg, "task_progress", {}).runtime, "openclaw");
   process.env.CLAWMANAGER_RUNTIME_TYPE = "opencode";
   cfg = protocol.readChannelConfig({});
   assert.equal(protocol.taskEvent(cfg, "task_progress", {}).runtime, "opencode");
  } finally {
   if (original === undefined) delete process.env.CLAWMANAGER_RUNTIME_TYPE;
   else process.env.CLAWMANAGER_RUNTIME_TYPE = original;
  }
  const source = await fs.readFile(output, "utf8");
  assert.ok(!source.includes('from "openclaw/plugin-sdk'));
  assert.ok(!source.includes("export default definePluginEntry"));
 } finally { await fs.rm(temporary, { recursive: true, force: true }); }
});
