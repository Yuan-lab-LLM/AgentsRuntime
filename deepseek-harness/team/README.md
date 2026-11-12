# DeepSeek Harness Team host

`plugin.mjs` is loaded only for explicitly managed Team Lite gateways. Ordinary
Lite and Pro native sessions do not start this consumer. Release capability is
`dsh-team-v1`, verified against the installed 0.2.1-rc.3 native package and host files.

The host uses the existing `opencode/team/bridge.mjs` and the authoritative Team
protocol built from `plugins/openclaw-redis-team/dist/index.js`. The shared bridge
only gains an optional display name, defaulting to OpenCode. Dispatch, work items,
dependencies, receipts, reminders and Leader root closure are unchanged.

All existing JSON tool fields come from that same protocol. `schema.mjs` translates
DSH's supported authoring subset: numeric/pattern constraints remain described and
validated by the server, disjoint union branches become `oneOf`, and optional fields
remain optional. Completion still requires only status and summary. Native identity
and generation come from the trusted transport and native tool context, never model
parameters. Only completed public assistant text supplements an accepted receipt.

Each immutable input has a deterministic native session and a private admission
journal. Persisted source and generation are checked independently of the journal.
An uncertain reservation is not replayed. Native errors before the first model request
are execution failures, not missed receipts. Disposal stops consumer admission, cancels
and joins the owned native execution, then releases the process-identity lock; unknown
stop evidence retains ownership. Finished handles are disposed while durable sessions
remain available. A normal browser or subagent session cannot acquire Team tools.

M2 checks: `node --test deepseek-harness/team/host.test.mjs` plus existing shared
bridge/outbox/recovery tests; `go -C deepseek-harness test -mod=readonly ./...`.
The image-native test runs `native-integration.mjs` with a disposable Redis endpoint.
It uses the real pinned DSH and Redis, a local model fixture and an explicitly controlled
completion acknowledgement. It proves tools, minimal receipt, artifact, public text,
presence and duplicate admission, **not** the CM production gate or a real model task.
M4 must separately use the final image and real CM/northbound Teams.

Build the full Lite Dockerfile or `Dockerfile.team` with a verified M1 base and a
freshly compiled current Agent supplied as the `agent-binaries` named context.
Do not advertise the capability on an image missing the adapter. Pro remains outside
Team admission. Do not enable global native slots to publish this host into a legacy
process-pool site; preserve that site's control and isolation contract.
