# OpenCode Team adapter

The governed OpenCode 2.0.27 delivery image supports Team Leaders and Workers.
The Lite and Pro images use the same native tools and Redis protocol.

`start-opencode-web` starts the member supervisor only when
`CLAWMANAGER_TEAM_ENABLED=true`. Ordinary instances keep the native serve path.
The final image must advertise `CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=opencode-team-v1`;
an unpatched image must not advertise this capability. ClawManager excludes
incompatible pools when scheduling OpenCode Team members.

The image packages the authoritative Redis Team implementation from
`plugins/openclaw-redis-team/dist/index.js`, without its OpenClaw host entry or
SDK import. It does not create a second Team state machine. The native v2 tools
are installed by `../model-governance/apply-governance.mjs`.

Each accepted transport delivery maps deterministically to a native root-task
session and input message. Redis deduplication, native input admission and the
existing business receipts protect replay. The local tool bridge checks the
authenticated native session, input and process generation; models do not need
to provide these fields. Same-process retries retain the native tool call result.
An initial managed Pro generation is zero; Lite Gateway generations start at
one. Zero is accepted only for an explicitly enabled Pro host with an instance
identity. The original generation is retained for governance and late-call fencing.
After a restart, the shared protocol's durable business receipts remain the
authority; an uncertain external operation is not declared successful.

Presence and assignment monitoring run independently of a model turn. The
supervisor reads the bounded materialized native message records for the exact
admitted input; older clients may use the durable event-log fallback. Reasoning
content and raw tool arguments/results are not projected. A deadline
interrupts the accepted native execution and checks actual idle before another
delivery can own the member. Unknown stop retains the current binding. Shutdown
stops inbox admission first and leaves unprocessed messages pending.

A native idle state, prose or file is never a completion receipt. Missing
completion is reported through the existing turn-outcome recovery protocol.
Worker delivery cannot close the Leader root. Shared files use the existing
member/task scope and preview service, including owner authorization.

For an owned, currently governed Team session, first-use web search selects
the configured Team provider (`CLAWMANAGER_TEAM_WEBSEARCH_PROVIDER`, default
`exa`) without a UI Form. Native permissions and explicit search disable still
apply; ordinary sessions retain consent, and the global provider selection is
not changed. Pending native permissions/forms are reported as interaction
waits without replaying execution or exporting request contents.

Targeted checks:

```sh
node --test opencode/team/*.test.mjs
REDIS_TEAM_TEST_URL=redis://127.0.0.1:6379 node opencode/team/redis-integration.mjs
```

The Redis test uses controlled native execution and does not claim real-model
acceptance. Delivery also requires the native source typecheck/tool tests,
actual image checks and mixed/all-OpenCode Team acceptance against the target
ClawManager backend. Existing busy OpenCode instances must not be moved between
pools using the old destructive shared-pool replacement path.

For a bridge-only rebuild on a verified native base, `opencode/Dockerfile.team-overlay`
repackages the authoritative protocol. Follow it with the governed
`model-governance/Dockerfile.prebuilt` artifact/version/hash checks. A marker on
a stock executable is insufficient. Preserve the full native UI archive when
reusing build artifacts: upstream `--skip-web-ui` omits that UI.

## Default release builds

Both `opencode/Dockerfile` and `opencode/Dockerfile.lite` build the native CLI
from the reviewed 2.0.27 commit with Bun 1.4.2 through
`model-governance/build-native.sh`. The build applies governance and native Team
tools, runs the native typecheck/tool tests (including Pro generation zero),
and includes the full UI. The final stage installs that executable and its
source manifest before advertising the Team marker. It no longer downloads
an unused stock CLI. Target-platform builder execution also validates the
matching architecture rather than attempting to execute an ARM artifact in
an amd64 build stage. The 2026-11-03 live delivery was validated on amd64;
this does not claim ARM live acceptance.

## Execution recovery maintenance (2026-11-04)

The full optional completion schema remains model-visible; only `status` and
`summary` are required. Known explicit completion aliases normalize before any
side effects. Validation failures are distinct from uncertain transport effects.
Native failures are not classified as missing business receipts.

A final public assistant text can supplement an already accepted result only
for the owned native input. It never creates a receipt or closes/reopens a root.
Intermediate text, reasoning and raw tool data stay private. Supplements use a
private publication journal under the existing member state directory: retry or
restart republishes the same event identity, never another model execution.

The independent control probe checks a retained execution identity after an
uncertain stop/read; it releases that identity only after native active/inbox
checks prove idle. A model is never started by this probe. The backend observes
all active roots, including Leaders without Worker cards, and applies bounded
same-root recovery only when execution exit is explicit. Unknown live executions
remain held and produce a visible blocker rather than a competing model turn.

For the backend-only and plugin-only image overlays use immutable serving bases;
record base/final digests and binary hashes with the release. The OpenClaw
maintenance overlay updates both installed/default plugin copies on Team10.2,
without changing its core, Agent, workspace adapters or instance identities.

Native 2.0.27's production bus does **not** retain historical events by default.
The supervisor therefore uses the materialized message API with an official
cursor anchored to the exact admitted input, bounded pages and a next-user/idle
boundary. Final public `finish=stop` text and native idle failure are distinct
facts. It never enables global event persistence or exposes reasoning. Changing
materialized record metadata/length supplies progress without publishing content.
Transient observation errors retain the current execution/envelope while probing;
they do not interrupt a healthy model or start a competing turn. The existing
execution deadline and confirmed-stop requirement remain.
