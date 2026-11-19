import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Package the existing, tested Team protocol without its OpenClaw host entry.
// Source remains authoritative in one place; no protocol fork or SDK imitation.
export async function buildProtocol(output, sourceFile) {
 const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
 let source = (await fs.readFile(sourceFile ?? path.join(root, "plugins/openclaw-redis-team/dist/index.js"), "utf8")).replaceAll("\r\n", "\n");
 const entry = "// ============ Plugin Entry ============";
 const sdk = 'import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";';
 if (source.split(entry).length !== 2 || source.split(sdk).length !== 2) throw new Error("Team protocol packaging boundary changed");
 source = source.slice(0, source.indexOf(entry)).replace(sdk, "");
 const names = ["runtimeStateDir", "activeContextMatches", "startActiveContextWatcher", "createRuntime", "startConsumer", "readChannelConfig", "isContextOnlyEnvelope", "appendLeaderTeamContext", "appendRedisTeamCompletionGuidance", "workflowReminderIsStale", "startAssignmentActivityObserver", "startAssignmentHeartbeat", "writeLocalStatus", "xaddJson", "taskEvent", "eventsKey", "RedisClient", "readTurnFacts", "mergeActiveTurnFacts", "activeMemberRouting", "observeTeamTurnOutcome", "turnFinishedWithoutCompletionEvent", "collectRootTaskArtifactRefs", "collectMemberAssignmentArtifactRefs", "validateArtifactRefs"];
 for (const name of names) if (!source.includes(`function ${name}(`) && !source.includes(`class ${name} `)) throw new Error(`Missing Team protocol export: ${name}`);
 await fs.writeFile(output, source + `\nexport { ${names.join(", ")} };\n`, { mode: 0o644 });
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
 if (!process.argv[2]) throw new Error("Protocol output path required");
 await buildProtocol(path.resolve(process.argv[2]), process.argv[3]);
}
