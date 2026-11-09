# Hermes Desktop Web Lite: implementation and acceptance

Read [the release guide](HERMES-LITE-RELEASE.md) for the upstream lock, local
compatibility patches, build commands, evidence requirements and release gates.
The versioned capability consumed by ClawManager is defined in the
[Desktop compatibility contract](HERMES-DESKTOP-COMPATIBILITY-V2.md).

## Deployment and scope

The separate `hermes/Dockerfile.lite` enables
`CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED=true` at deployment time. The create request
cannot enable/disable this switch, select an executable, or choose a backend mode.
The mode is `dashboard`; an unknown deployment mode, including the unverified
`serve`, fails gateway creation. Team assignments are rejected in this image.
The existing Pro/Team image and launcher remain independent. The shared registry
publisher does not promote the candidate; the new CI workflow builds and retains
verification artifacts without publishing.

Required runtime configuration:

| Setting | Requirement |
| --- | --- |
| `RUNTIME_AGENT_CONTROL_TOKEN`, `RUNTIME_AGENT_REPORT_TOKEN` | Managed Pod secrets; never sent to Hermes children |
| `CLAWMANAGER_BACKEND_URL` | Kubernetes Service DNS; supplies the trusted HTTP `Date` clock reference |
| `CLAWMANAGER_RUNTIME_IMAGE_REF` | Image reference reported for diagnostics; tags and digests are accepted |
| `RUNTIME_AGENT_DATA_DIR` | Agent-owned private directory outside `/workspaces` |
| `CLAWMANAGER_CONTROL_UI_ORIGIN` | One explicit trusted origin used by the BFF upstream connections |
| `CLAWMANAGER_TRUSTED_PROXY_CIDRS` | Explicit BFF/proxy addresses or CIDRs; no wildcard or `/0` |
| `RUNTIME_GATEWAY_PORT_START/END` | Validated port pool, candidate defaults `20000–20299` |
| `RUNTIME_GATEWAY_CAPACITY` | Conservative default `100`; increase only after capacity acceptance |

For individual gateway startup, the two proxy settings may alternatively come
from the existing controlled request `environment` map, with the same validation.
Global Desktop capability requires verified deployment-level values: the Origin
must use internal Kubernetes Service DNS and the trusted proxy CIDRs must be
explicit. Request-only settings cannot enable this profile-wide capability.
No runtime registration or
heartbeat fields were invented. The agreed optional health
capability uses contract version 1 as described below. The ClawManager feature
flag must remain off until real BFF/browser acceptance is complete.

Each ordinary create request supplies its assigned `gateway_port`, positive
instance/user/generation/UID/GID, exact workspace, and instance LLM URL/token.
The LLM URL must use Service DNS. Existing `CLAWMANAGER_LLM_*` / OpenAI aliases
remain supported, but there is no Pod/global key fallback. Dashboard password
priority is explicit `HERMES_DASHBOARD_BASIC_AUTH_PASSWORD`,
`CLAWMANAGER_DASHBOARD_BASIC_AUTH_PASSWORD`, `CLAWMANAGER_INSTANCE_ACCESS_TOKEN`,
then `CLAWMANAGER_INSTANCE_TOKEN`; username defaults to `clawmanager`.
Hash-only credentials cannot establish the agent's authenticated readiness probe.
Qualified model providers use the managed `clawmanager-*` namespace so upstream
built-in provider selection cannot substitute another endpoint or credential.
Their routing fields override stale endpoint/key-command aliases; saved custom
credential pools do not override the current instance token in this Lite image.

## Versioned Desktop Core capability

`GET /v1/health` keeps its control-token authentication, GET-only behavior,
`manager.Health()` failure response and `ready`/`draining` status. A Runtime may
implement an optional capability provider; existing profiles need no new method.
The manager copies the provider's immutable snapshot during initialization.
Repeated health reads do not create gateways, inspect histories or call models.

The optional `capabilities.hermes_desktop_web` object has exactly the agreed
version 1 identity: `contract_version=1`, `enabled=true`,
`hermes_ref=v2026.8.31`, `hermes_commit=29112bef099274229cadff79cdff7bf7b99c4b77`,
`rpc_protocol=hermes-jsonrpc-v1`, `backend_mode=dashboard`, and
`auth_mode=password-cookie`. The RPC identifier is different from the public
WebSocket handshake subprotocol `hermes-gateway-v1`.

Hermes only supplies this snapshot after validating the selected Lite launcher,
deployment configuration and image-owned release evidence at
`/usr/local/share/hermes-lite/release.json`. The source lock remains a separate
`source-lock.json`. Missing, incompatible, unaccepted or modified metadata cannot
enable Desktop. Evidence files and their ancestors must be root-owned and not
group/world writable; symlink paths and unbounded inventories are rejected.
Verification happens once at initialization, before serving the snapshot.

Capability describes the profile's protocol, independently of gateway count or
any individual Team assignment. Instance generation, status, ownership and Team
eligibility still require CM checks. Drain continues to refuse new creates even
when an already accepted capability is present. Missing optional Desktop evidence
does not by itself turn a healthy legacy Runtime into a 503.

The current new candidate remains **unaccepted** until the real CM BFF/browser
gate passes, so it must omit this object. Do not set a version environment variable
or edit an accepted Boolean to open the UI. Capability alone never certifies the
appearance or completeness of CM's Desktop renderer.

Production deployments should pin the container image to a digest when practical,
but the Runtime does not enforce that policy through
`CLAWMANAGER_RUNTIME_IMAGE_REF`. That value is self-reported deployment metadata
and cannot prove which image the container runtime actually started. Image policy
belongs in deployment tooling or cluster admission controls; Desktop capability
continues to rely on the build-owned release record and verified artifact hashes.

## Lite execution and environment boundary

The Lite-only patches remove `desktop_ui`, `desktop_project` and `computer_use`
from actual model tool definitions and reject their execution before shared,
inline or concurrent dispatch, including tool aliases. Native callbacks cannot
wait for an absent Electron renderer. Terminal and clarification tools remain
available under the instance UID, workspace and existing approval policy.

New and cold-restored agents are checked and sealed. Warm reuse, concurrent
resume winners and transport reattachment reject an incompatible existing agent
with a fixed error, preserving its running work, transport and history. Merely
changing a session's `source` metadata is insufficient.

External `/api/ws` clients receive the Core method and parameter allowlist:
ping; session create/list/status/resume/history/events.since/interrupt;
prompt.submit; approval.respond once/deny; clarify.respond. Arbitrary profile,
cwd, shell, configuration and native management RPCs are rejected. The fixed
classic TUI is identified by its verified loopback credential, not by a client
supplied source string. All WS paths only select an offered public protocol,
never a credential-bearing protocol.

Lite policy is fixed in the Lite artifact and cannot be disabled through a
workspace `.env`. The patched dotenv loader preserves initial process-owned
identity, home, paths, port, authentication, Origin/proxy configuration and
managed OpenAI aliases before assignment; unknown user keys keep their normal
dotenv semantics. These patches are not installed in independent Pro/Team images.

## Process and configuration behavior

- Create returns `starting` before workspace work and process readiness. Same
  generation retries return the same record. A newer generation cancels and waits
  for all prior workspace work and process-group cleanup before writing or starting.
- Delete cancels queued starts, waits for process-group shutdown, then releases
  the port. Failed termination retains both the process handle and reservation;
  deletion can retry. Drain rejects new creates while existing gateways are reported.
- SIGTERM drains and stops gateways, attempts final reports, and retains workspaces.
  Private process metadata records boot ID, PID, UID and kernel start ticks.
  Startup removes stale metadata and stops only proven matching old process groups.
  An unverifiable live group blocks initialization instead of risking another process.
- Workspace access uses directory handles rooted in the exact instance, rejects
  symlinks, and sets requested ownership. Config files, credentials, backups and
  version markers use `0600`, bounded reads and atomic rename.
- YAML, JSON and dotenv values are merged structurally, preserving unknown user
  fields, including values inherited through YAML anchors and merge keys.
  Platform-owned fields are routing credentials, managed provider routing,
  dashboard authentication, public URL, trusted proxies, and gateway metadata.
  An incompatible version marker fails without clearing config or sessions.
- The child receives an explicit environment whitelist, fixed instance homes and
  exact port. It uses `exec hermes dashboard ... --isolated`; there is no second
  instance agent, Team worker, background business listener or shell command template.

Existing scheduled-task files and sessions are retained. This candidate does not
apply new Cron/Team resource injection through the old global-environment writer;
advanced features require their separate acceptance and authorization design.

## Health, diagnostics, and network

Readiness requires the live supervised process, a Linux listening inode owned by
its process group, readable managed config, instance LLM credentials, exact Hermes
version and enforced authentication, password login/session validation, and an
authenticated `/api/events` WS handshake plus ping/pong. The probe uses a one-time
ticket in the WebSocket subprotocol, without URL credentials or user-session RPCs.
When normal container permissions prevent root from reading another UID's socket
links, a bounded read-only helper checks them as the verified instance UID; no
extra container capabilities are required.
The actual Desktop `/api/ws` JSON-RPC workflow still requires BFF acceptance.
HTTP/WS health and process ownership are checked again every 15 seconds; a failure
marks the instance unhealthy and terminates it before releasing its port.

The internal backend HTTP `Date` is sampled before initialization and refreshed
during heartbeats. More than 30 seconds offset produces a safe warning; more than
120 seconds prevents ready/running reporting. A previously verified clock sample
remains valid for up to five minutes through transient reference outages. After
that, admission and ready/running reports are blocked until the reference returns;
reference outages alone do not kill existing gateways. No container clock is adjusted.

Control/status errors expose fixed categories. Third-party stdout/stderr is
suppressed because newly generated credentials cannot be reliably redacted by
matching known environment values. Structured lifecycle messages and recognized
network-boundary audit categories include instance ID and generation without
reflecting request headers, bodies or URLs.

[The NetworkPolicy example](deploy/lite-network-policy.example.yaml) must be
adapted to actual namespaces/selectors and tested with the deployed CNI. It
permits only selected internal backend/gateway Pods on the control and assigned
gateway pool ports. Do not expose gateway ports through public Services, Ingress,
NodePort, host networking or hostPort. Keep BFF ownership checks and API allowlists
in ClawManager; a trusted CIDR does not prove user authorization.

## Validation and rollback

Run the shared Go suite on Linux, including race tests for the gateway lifecycle,
listener ownership and process recovery. Python tests validate the immutable lock
and Lite source patches. Build and run the real image for protocol smoke checks.
These tests complement, and do not replace, the real browser/BFF, old-workspace
upgrade/rollback, negative network/authorization, and configured-capacity tests
listed in the release record.

Before rollout, record the image digest, SBOM, ClawManager revision, Hermes Desktop
renderer revision, source lock, test evidence, resource curve and rollback digest.
An upgrade of an unmarked workspace preserves pre-migration config backups ending
in `.clawmanager-pre-desktop-web.bak`; it never replaces an existing backup. A new
marked version needs an explicit tested migration. Rollback changes the image
digest and ClawManager feature flag, preserving the entire workspace. Do not delete
the marker or configuration to force an incompatible image to start.

The platform contract documents belong to the sibling ClawManager checkout and
are not copied into this repository. This revision follows the user's Desktop
Core repair handoff and its exact version 1 health and HTTP/WS contract, together
with the existing runtime integration guide and source contracts. Documentation
availability does not establish real CM BFF/browser acceptance; that gate remains
pending as recorded above.
