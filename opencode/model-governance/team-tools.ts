export * as ClawManagerTeamTools from "./clawmanager-team-tools.js"
import type { Context } from "@opencode/plugin/effect/plugin"
import { ToolFailure } from "@opencode/ai"
import { Effect, Schema } from "effect"
import { Governance } from "./clawmanager-governance.js"
import { Permission } from "./permission.js"
import { WebSearch } from "./websearch.js"
import { SessionSchema } from "./session/schema.js"

// Only the bridge-owned Team session gets a non-interactive default. Ordinary
// sessions retain native consent; native permission checks and explicit search
// configuration/disable still run before this ProviderRequired fallback.
export const searchProvider = Effect.fn("ClawManagerTeamTools.searchProvider")(function* (sessionID: SessionSchema.ID, sessionInfo: Effect.Effect<SessionSchema.Info | undefined, unknown>, websearch: WebSearch.Interface) {
 if (process.env.CLAWMANAGER_TEAM_ENABLED !== "true") return undefined
 const team = process.env.CLAWMANAGER_TEAM_ID
 const member = process.env.CLAWMANAGER_TEAM_MEMBER_ID
 if (!team || !member) return undefined
 const session = yield* sessionInfo
 if (session?.parentID || session?.metadata?.["clawmanager.team"] !== team || session.metadata["clawmanager.member"] !== member) return undefined
 const headers = yield* Governance.headers
 if (headers["X-ClawManager-Session-ID"] !== sessionID || !headers["X-ClawManager-Task-ID"]) return undefined
 const provider = process.env.CLAWMANAGER_TEAM_WEBSEARCH_PROVIDER || "exa"
 const available = yield* websearch.providers()
 if (!available.some(item => item.id === provider))
  return yield* Effect.fail(new ToolFailure({ message: "The configured Team search service is unavailable. No interactive approval or search was started." }))
 return provider as typeof available[number]["id"]
})

const optionalFields = {
 status: Schema.optional(Schema.String), summary: Schema.optional(Schema.String),
 resultMarkdown: Schema.optional(Schema.String), text: Schema.optional(Schema.String),
 message: Schema.optional(Schema.String), to: Schema.optional(Schema.String),
 memberId: Schema.optional(Schema.String), progress: Schema.optional(Schema.Number),
 eventKind: Schema.optional(Schema.String), intent: Schema.optional(Schema.String),
 scope: Schema.optional(Schema.Literals(["member", "team"])), kind: Schema.optional(Schema.Literals(["plan", "context", "review", "final"])),
 path: Schema.optional(Schema.String), content: Schema.optional(Schema.String),
 artifactRefs: Schema.optional(Schema.Array(Schema.String)),
 reviewVerdict: Schema.optional(Schema.String), reviewedAssignmentId: Schema.optional(Schema.String),
 reviewedRevision: Schema.optional(Schema.Number), reviewedArtifactRefs: Schema.optional(Schema.Array(Schema.String)),
}
const Input = Schema.StructWithRest(Schema.Struct(optionalFields), [Schema.Record(Schema.String, Schema.Unknown)])
const CompletionInput = Schema.StructWithRest(Schema.Struct({
 ...optionalFields,
 status: Schema.Literals(["succeeded", "failed", "cancelled", "completed", "done", "success"]),
 summary: Schema.String,
}), [Schema.Record(Schema.String, Schema.Unknown)])
class TeamArgumentError extends Error {}
const descriptions = {
 team_send: "Use to and text to send a real assignment, handoff, question or blocker to a Team member. Do not repeat an existing assignment. Use team_complete_task for delivery.",
 team_status: "Read current Team member status and the current root's accepted assignment results. Shared task files are dispatch snapshots, not the current ledger. Do not reassign work already accepted or running.",
 team_update_progress: "Report actual progress with status and summary. Active assignment identity is inherited automatically.",
 team_complete_task: "Submit actual work using status=succeeded, failed or cancelled and summary. Completed/done/success are accepted success aliases. Put the full answer in resultMarkdown and actual artifactRefs only when applicable; a text answer needs no file. Assignment identity is inherited. Ordinary replies do not complete Team work. Worker delivery does not complete the Leader root. Reuse existing results when correcting a missed receipt.",
 team_artifact_read: "Read a UTF-8 file in this Team shared workspace using path.",
 team_artifact_write: "Atomically write a UTF-8 Team artifact using path and content. A simple file name uses the current member/assignment. For a Leader plan or final report use scope=team and kind=plan or final; the corresponding directory is added automatically. Reuse exact canonical paths returned by tools; never invent or duplicate a prefix.",
 team_artifact_list: "List this Team's artifacts using an optional path.",
 team_artifact_mkdir: "Create a member-scoped Team artifact directory using path.",
 team_artifact_preview: "Get a signed read-only Team file preview using path. Do not run temporary preview servers.",
}
export const Plugin = {
 id: "clawmanager.tool.team",
 effect: Effect.fn("ClawManagerTeamTools.Plugin")(function* (ctx: Context) {
  if (process.env.CLAWMANAGER_TEAM_ENABLED !== "true") return
  const port = Number(process.env.CLAWMANAGER_TEAM_BRIDGE_PORT)
  const token = process.env.CLAWMANAGER_TEAM_BRIDGE_TOKEN
  const generation = Number(process.env.CLAWMANAGER_GATEWAY_GENERATION)
  const managedPro = process.env.CLAWMANAGER_AGENT_ENABLED === "true" && /^[1-9][0-9]*$/.test(process.env.INSTANCE_ID || "")
  if (!Governance.enabled || !Number.isInteger(port) || port < 1 || port > 65535 || !token || !Number.isSafeInteger(generation) || generation < 0 || generation === 0 && !managedPro)
   return yield* Effect.die(new Error("OpenCode Team adapter is not ready."))
  const permission = yield* Permission.Service
  yield* ctx.tool.transform(editor => {
   for (const [name, description] of Object.entries(descriptions)) editor.add({
    name, description, options: { codemode: false }, input: name === "team_complete_task" ? CompletionInput : Input, output: Schema.Unknown,
    execute: (args, context) => Effect.gen(function* () {
     yield* permission.assert({ action: name, resources: [process.env.CLAWMANAGER_TEAM_ID || ""], save: ["*"], metadata: {}, sessionID: context.sessionID, agent: context.agent, source: { type: "tool", messageID: context.messageID, id: context.id } }).pipe(Effect.mapError(() => new ToolFailure({ message: "Team tool permission denied." })))
     const identity = yield* Governance.headers
     if (identity["X-ClawManager-Session-ID"] !== context.sessionID)
      return yield* Effect.fail(new ToolFailure({ message: "Team session identity mismatch." }))
     const output = yield* Effect.tryPromise({
      try: async signal => {
       const response = await fetch(`http://127.0.0.1:${port}/tool`, {
        method: "POST", redirect: "error", signal: AbortSignal.any([signal, AbortSignal.timeout(30000)]),
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ name, arguments: args, sessionID: context.sessionID, taskID: identity["X-ClawManager-Task-ID"], generation, callID: context.id }),
       })
       if (!response.body) throw new Error()
       const reader = response.body.getReader()
       const chunks: Uint8Array[] = []
       let size = 0
       try {
        while (true) {
         const { done, value } = await reader.read()
         if (done) break
         size += value.byteLength
         if (size > 4 * 1024 * 1024) throw new Error()
         chunks.push(value)
        }
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"))
        if (!response.ok) {
         if (response.status === 422 && body?.error_code === "TEAM_INVALID_ARGUMENT")
          throw new TeamArgumentError("The Team path or arguments are invalid. Use a simple file name under the current assignment, or scope=team and kind=plan/final for Leader reports. Nothing was published by this rejected call.")
         if (response.status === 422 && body?.error_code === "TEAM_INVALID_COMPLETION")
          throw new TeamArgumentError("Use status=succeeded, failed or cancelled and a non-empty summary. Task identity is inherited; a text answer needs no file.")
         throw new Error()
        }
        return body
       } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
      },
      catch: error => new ToolFailure({ message: error instanceof TeamArgumentError ? error.message : "Team operation failed or result is unconfirmed. Inspect the original work before retrying." }),
     })
     return { output, content: [{ type: "text" as const, text: JSON.stringify(output) }] }
    }),
   })
  })
 }),
}
