import { spawn } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";
import { createBridge, NativeClient } from "./bridge.mjs";
import * as protocol from "./protocol.mjs";

if (process.env.CLAWMANAGER_TEAM_ENABLED !== "true") throw new Error("Team supervisor requires an explicit Team identity");
if (process.env.CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION !== "opencode-team-v1") throw new Error("OpenCode Team requires a compatible governed Runtime image");
process.umask(0o002);
const native = new NativeClient({ port: Number(process.env.CLAWMANAGER_GATEWAY_PORT || process.env.PORT), password: process.env.OPENCODE_SERVER_PASSWORD, username: process.env.OPENCODE_SERVER_USERNAME });
const bridge = await createBridge({ protocol, native });
const child = spawn("/usr/local/bin/opencode", ["serve", ...process.argv.slice(2)], {
 stdio: "inherit", env: { ...process.env, CLAWMANAGER_TEAM_BRIDGE_PORT: String(bridge.port), CLAWMANAGER_TEAM_BRIDGE_TOKEN: bridge.token, CLAWMANAGER_TEAM_CONTEXT_KEY: bridge.contextKey },
});
let exiting = false;
async function shutdown(signal) {
 if (exiting) return;
 exiting = true;
 try { await bridge.stop(); child.kill(signal); }
 catch { console.error("OpenCode Team stop could not be confirmed; retaining execution for Runtime supervision"); process.exitCode = 1; }
}
process.on("SIGTERM", () => void shutdown("SIGTERM"));
process.on("SIGINT", () => void shutdown("SIGINT"));
function nativeExited(code) {
 exiting = true;
 void bridge.stop({ nativeExited: true }).catch(() => { console.error("OpenCode Team cleanup failed"); }).finally(() => { process.exitCode = code ?? 1; });
}
child.on("error", () => { console.error("OpenCode Team native process failed to start"); nativeExited(1); });
child.on("exit", nativeExited);
try {
 const deadline = Date.now() + 90000;
 while (!exiting) {
  try { const info = await native.api("/api/info"); if (!String(info.version).startsWith("2.") || !(info.pid > 0)) throw new Error(); break; }
  catch { if (Date.now() >= deadline) throw new Error("OpenCode Team native readiness timed out"); await sleep(500); }
 }
 if (!exiting) await bridge.start();
} catch { console.error("OpenCode Team startup failed"); await shutdown("SIGTERM"); }
