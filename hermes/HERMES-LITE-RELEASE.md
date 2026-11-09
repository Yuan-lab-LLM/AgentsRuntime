# Hermes Lite candidate: v2026.8.31

2026-09-08 compatibility update: health capability contract **2** separates
installed protocol compatibility from signed release acceptance. A configured
Lite runtime may advertise `enabled: true`, `artifacts_verified: true` and its
`payload_sha256` after verifying the complete managed image inventory, even while
`release_accepted: false`. `release.json` keeps schema 2, protocol contract 1 and
the truthful `desktop_web_accepted: false`; no browser campaign is inferred.
An image claiming `desktop_web_accepted: true` must still pass the full signed
campaign and promotion checks. Missing/ambiguous acceptance state, inconsistent
acceptance metadata, unsafe deployment configuration or modified/untrusted
artifacts suppress the capability. CM contract 2 consumers must check the
explicit integrity fields. Existing contract 1 capabilities describe the former
accepted-only behavior. Historical candidate/deployment records below retain the
behavior of the image they identify; they do not claim this change is deployed.

This image is an implementation candidate, not an accepted Desktop Web release.
Use `hermes/Dockerfile.lite`; the existing `hermes/Dockerfile*` Webtop/Pro images
retain their separate behavior. The shared publishing workflow deliberately does
not publish the new Hermes Lite candidate before the acceptance gates below.

## Verified upstream identity

Verified on 2026-09-07 and rechecked on 2026-09-08 against the official upstream:

| Item | Reviewed value |
| --- | --- |
| [Agent release](https://github.com/NousResearch/hermes-agent/releases/tag/v2026.8.31) | `v2026.8.31` |
| Annotated tag object | `6e8f8418e6378eb2617e4de074e13dedd091b8af` |
| [Peeled source commit](https://github.com/NousResearch/hermes-agent/tree/29112bef099274229cadff79cdff7bf7b99c4b77) | `29112bef099274229cadff79cdff7bf7b99c4b77` |
| `pyproject.toml` / CLI package | `0.21.0` / release date `2026.8.31` |
| Root / classic Web / shared / TUI Node package | `1.0.0` / `0.0.0` / `0.0.0` / `0.0.1` |
| Node runtime | `22.23.2` |
| Source archive SHA-256 | `76b99a8be9b77d66833c3cfe2b35c6d6f6a58e4ff9637ef8effcfc1f420ab35a` |
| ClawManager image observed on 172 (2026-09-08) | `sha256:0e8c6d6cf6571fd30d6d97ee91aaf046758edd113caef8c9d6f11a4a857c50bd`; embedded revision `3216a4f34f0a49f700d328ee1a4013df1b0b98c6-dirty` |
| Hermes Desktop used by ClawManager | Package `0.17.0`, source commit `29112bef099274229cadff79cdff7bf7b99c4b77`, original `apps/desktop/src/main.tsx` entry |
| Observed renderer build input | `465fb5f1b530cfd155e7e44266fa9f020bfb565c5c0dd544439f8d6f73d2f0d8` |
| Accepted published image digest | **Not produced** |

The CM rows are historical read-only deployment identities, not browser
acceptance. Deployment-specific evidence belongs in release artifacts rather
than this source tree. The embedded dirty revision does not identify uncommitted
CM source.

`hermes-lite.lock.json` also pins the npm lockfile, uv lockfile, metadata files,
and the exact server/TUI/provider sources to which the local patch applies. The Node
package versions differ from the Python version; the verifier
checks their recorded values rather than assuming equality. The official tag
is annotated: its object SHA is not the checked-out commit SHA.

## Image boundary and build

The image runs one shared runtime agent as PID 1's child. It contains no
Electron, Webtop, X11, Desktop renderer, browser bridge, or ClawManager frontend.
It builds Hermes' existing classic Dashboard and its terminal chat TUI for fallback
on the same instance port. These are not the Electron/Desktop renderer. Node is
retained for the TUI and supported tool execution; npm, Go, uv and
their build caches do not enter the final stage. MIT license and upstream source
copyright notices are retained. Python uses upstream's required editable source
install and `uv sync --locked --no-dev --extra web`; Web/TUI use `npm ci` with the
upstream lockfile and only the `web`/`ui-tui` workspaces. The TUI is bundled into
one JavaScript file and needs no runtime node_modules. SBOM generation additionally
scans the Dashboard build stage so bundled npm dependencies remain inventoried.

```bash
python -m pip install --requirement hermes/tests/requirements.txt
python -m unittest discover -s hermes/tests -p 'test_lite_*.py'
python hermes/scripts/verify_lite_release.py \
  --lock hermes/hermes-lite.lock.json --upstream
docker buildx build --file hermes/Dockerfile.lite \
  --platform linux/amd64 --sbom=true --provenance=mode=max \
  --build-arg AGENT_REVISION="$(git rev-parse HEAD)" \
  --build-arg AGENT_VERSION="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --metadata-file hermes-lite-build.json \
  --output type=oci,dest=hermes-lite.oci.tar \
  --tag agentsruntime-hermes-lite:candidate .
```

The unified `docker-ghcr.yml` matrix builds and publishes Hermes Lite from
`hermes/Dockerfile.lite` together with every other Pro and Lite runtime image.
The Dockerfile verifies the locked upstream source, reviewed patches, test
artifacts and generated release metadata while building. Run the host-side tests
and fixed candidate suites described here before deploying a published digest.
For an arm64 deployment, test that architecture separately before promotion.

## Managed release evidence

`/usr/local/share/hermes-lite/source-lock.json` pins upstream archive/source hashes,
all six reviewed patch files, the six fixed acceptance scripts and their dispatch helper. The build
verifies original source hashes, applies the network boundary followed by the
non-native backend and tool-executor patches, and verifies the local patch/test hashes before assembling
the final image. `/usr/local/share/hermes-lite/release.json` uses schema 2 and
inventories the actual source, installed Python dependencies/interpreter, Web/TUI
assets, Node, agent binary, launchers, patch sources and test scripts. Every entry
is a root-owned regular file with no writable group/world bits or symlink path
components. The temporary uv `.venv/.lock` file is removed before assembly.

The payload hash is SHA-256 of the sorted UTF-8 sequence
`absolute_path + NUL + file_sha256 + LF`. It excludes release metadata and evidence,
avoiding a circular dependency on the image's own digest. The build always writes
`desktop_web_accepted: false`; the old lock Boolean has no authority to enable a
capability. A missing, malformed, changed, writable or incomplete accepted manifest
causes the optional Desktop capability to be omitted. It does not turn ordinary
process health into a Desktop acceptance claim.

`run_release_check.py` executes the five local scripts inside the exact candidate
with `--network none` and records their real exit codes and output. It verifies that the
local image matches the OCI index or its content-addressed platform config, and
that its own runner hash matches the image. Reports bind the candidate digest,
payload hash, script path/hash, runner hash, timestamps, and the actual output log
hash. Schema 2 reports also bind the exact `acceptance-binding.json` bytes. Local
CI runs without a binding retain `binding_sha256: null` and cannot be accepted.
Existing evidence files are never overwritten. The mandatory suites are
`lite_image`, `lite_provider`, `lite_agent`, `lite_non_native`, `desktop_rpc` and
`cm_bff_browser`. The last suite extracts the fixed Python/JavaScript browser
harness from the immutable candidate and verifies both script hashes before
executing it on the controlled browser host. Only an explicit execution config
and binding are accepted; no external command or script may be supplied. The
browser binary, Node and Playwright contents are checked against the binding.
The gate fails when a real, authorized isolated CM/Runtime test target is absent.
Synthetic protocol fixtures cannot satisfy it.

For each available local suite, after loading the same OCI candidate:

```bash
python hermes/scripts/run_release_check.py --image agentsruntime-hermes-lite:candidate \
  --metadata hermes-lite-build.json --oci hermes-lite.oci.tar \
  --suite desktop_rpc --output evidence/desktop_rpc.json
```

The 2026-09-08 acceptance protocol separates the real networked campaign from
offline image promotion. `run_campaign.py` must execute all six suites and observe
the actual CM and Runtime identities before and after the run. The binding includes
the candidate OCI index, platform manifest, config and payload; CM image and
provenance; the fetched renderer asset manifest; fixed campaign/browser sources;
and the actual host browser, Node and Playwright identities. Both observation
records, the six reports/logs, and the browser result/artifacts must agree.

Only that completed campaign calls `campaign_signing.sign_completed_campaign`.
There is no CLI for signing supplied reports. The fixed Ed25519 public key is
`release-trust.json`, itself covered by the source lock and candidate inventory.
Its key ID is `agentsruntime-local-20260908`, with origin `local-operator`.
The private key remains outside the repository, build context and image.
The signature covers the domain `hermes-lite-acceptance-v1` followed by a newline
and the exact decoded signature payload bytes. The payload binds the binding,
every report/log digest and both observation digests. The reports additionally
bind the complete browser result and its bounded, safe artifact inventory.

`Dockerfile.lite.accept` derives from the **exact immutable tested candidate**.
With networking disabled it verifies the public-key signature and all six campaign
reports, then reruns the five fixed local suites and verifies the payload again.
Every `promotion-<suite>.json/.log` is mandatory and independently validated;
its phase, binding, source/runner, output, timestamps and exit code must match.
The external browser suite is verified through signed evidence rather than
incorrectly attempting to reach CM from `--network none`. The on-disk candidate
stays unaccepted until every check passes. Use `--no-cache-filter promotion` to
ensure each promotion attempt actually reruns the local suites.

An accepted build must retain its own OCI archive, SBOM/provenance and digest.
Retain the candidate's Dashboard-builder SBOM alongside the accepted image's
runtime SBOM: promotion does not rebuild the bundled Node dependency workspace.
Before publishing, run `verify_lite_promotion_oci.py` against both archives. It
checks the content-addressed graph, exact base-layer prefix, unchanged runtime
config, signed evidence and five promotion reports. Added layers may contain
only the release record, fixed evidence paths and bounded browser artifacts;
runtime files, whiteouts, links, traversal and unrelated directories are rejected.

```bash
docker buildx build --file hermes/Dockerfile.lite.accept --platform linux/amd64 \
  --build-arg CANDIDATE="$IMMUTABLE_CANDIDATE_REF" --no-cache-filter promotion \
  --sbom=true --provenance=mode=max --metadata-file accepted-build.json \
  --output type=oci,dest=accepted.oci.tar "$CAMPAIGN_CONTEXT"
python hermes/scripts/verify_lite_promotion_oci.py \
  --candidate-oci candidate.oci.tar --candidate-digest "$CANDIDATE_DIGEST" \
  --accepted-oci accepted.oci.tar --accepted-digest "$ACCEPTED_DIGEST" \
  --output accepted-ancestry.json
```

This is local-operator signing, not a claim of independent CI attestation. The
operator holding the private key, the execution host and the reviewed image
builder remain trusted boundaries; a malicious signer can lie about execution.
Handwritten or modified evidence without the trusted signature cannot promote.
PR CI has no signing key or real browser credentials and produces candidates only.
Any change to a harness, patch, policy, binary or other managed artifact requires
a new candidate and new execution reports. Synthetic positive unit fixtures are
validator tests and are never production acceptance evidence. Historical local
candidates below remain unaccepted; the new mechanism does not upgrade them.
The file manifest validates listed managed regular files. It is not a complete
OS-file or added-file/symlink intrusion detector; those remain within the trusted
root and immutable-image boundary and the complete image digest/SBOM inventory.

## Audited local patch

The pinned upstream [`web_server.py`](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/hermes_cli/web_server.py)
allows any Host on a wildcard bind, and reuses that check for WebSocket Origin.
`dashboard.trusted_proxies` alone does not impose an exact Origin allowlist.

`apply_lite_gateway_boundary.py` refuses any server whose SHA-256 differs from
the lock. It adds `lite_gateway_boundary.py` as an outer ASGI boundary and disables
Uvicorn's forwarded-address rewriting so the boundary sees the real socket peer.
The patch applies only to this Lite image. It:

- Accepts only configured proxy CIDRs and loopback readiness probes.
- Rejects forwarded headers unless the socket peer is in the configured CIDRs.
- Compares complete Origins including scheme and port against the deployment
  `CLAWMANAGER_CONTROL_UI_ORIGIN`; it rejects wildcard, duplicate and malformed Origins.
- Requires Origin on WebSocket and non-read-only HTTP requests. The server-spawned
  TUI uses a separate process-local credential in a WebSocket subprotocol; only
  authenticated loopback clients can have their missing Origin normalized.
- Rejects native console, SSH, cloud, Desktop and self-update endpoint families, with safe
  audit categories. Disables the upstream access log to keep URL queries out of logs.
- Keeps `/api/pty` solely for classic Hermes Chat: fixed image-owned Node/TUI argv,
  known chat parameters and current profile only. User-selected executables, raw
  shell/terminal variants and cross-profile requests are rejected.
- Removes upstream's `?internal=` credentials from TUI gateway/event URLs. Internal
  gateway and event WebSockets reconnect with the server-only subprotocol value;
  the reply selects only the public stable protocol. Credential query strings are
  rejected, including the upstream browser `?ticket=` fallback. ClawManager's BFF
  must use server-owned authentication and subprotocol forwarding.
- Leaves Hermes password login, session cookies and one-time WS ticket verification
  active behind the boundary. Rejections return fixed categories, never headers.
- Skips upstream saved credential-pool precedence only for the managed `clawmanager`
  and `clawmanager-*` provider namespace when the Lite flag is enabled. The managed
  config's instance `key_env` stays authoritative; saved auth files and other
  providers retain their existing behavior. The resolver patch also has an exact
  source checksum guard and a real offline test with clean and existing pools.

Both `CLAWMANAGER_CONTROL_UI_ORIGIN` and comma-separated
`CLAWMANAGER_TRUSTED_PROXY_CIDRS` must be deployment-controlled and passed to each
gateway. Missing values, malformed CIDRs or `/0` fail closed. NetworkPolicy and
Service policy must still restrict gateway access to the BFF; an IP range alone
does not establish instance ownership. Local probes use no forwarded headers.

## Protocol observations, not BFF acceptance

Source inspection confirms `hermes dashboard --host --port --no-open --skip-build`
and `--isolated` in `hermes_cli/subcommands/dashboard.py`. `serve` shares the backend
but suppresses the SPA; it is not enabled here. `--isolated` prevents the new
unified profile launcher from attaching an instance to another server.

The [password provider](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/plugins/dashboard_auth/basic/__init__.py)
uses `dashboard.basic_auth` or `HERMES_DASHBOARD_BASIC_AUTH_*` environment overrides.
Despite its name, it authenticates through a JSON password login and session
cookies, not arbitrary HTTP Basic Authorization. Credentials must stay in BFF
server memory/restricted storage. A process-local signing secret invalidates
sessions on restart unless an explicit per-instance signing secret is supplied.

The [auth routes](https://github.com/NousResearch/hermes-agent/blob/29112bef099274229cadff79cdff7bf7b99c4b77/hermes_cli/dashboard_auth/routes.py)
include `POST /auth/password-login`, `GET /api/auth/me`, and
`POST /api/auth/ws-ticket`. `GET /api/health` is public and alone cannot prove
authentication. A read-only `/api/events` WebSocket can validate an authenticated
one-time ticket carried in `Sec-WebSocket-Protocol`, with no credential query string.
The stream's protocol and close/reconnect behavior must still be captured through
the actual ClawManager BFF. `/api/ws` may start an orphan-session sweep and is not
used as a readiness probe.

Actual prefix handling uses `dashboard.public_url` and trusted
`X-Forwarded-Prefix`; existing `gateway.json.base_path` is platform metadata and is
not sufficient evidence of upstream routing. BFF must strip the external prefix
before forwarding and supply a validated prefix header, and retain upstream
cookie/session state entirely on the server.

## Required release gates

### Signed acceptance pipeline: local candidate (2026-09-08)

After candidate publication, the same `013ef57b…` candidate was deployed and
passed Runtime checks. Deployment-specific backup and rollout evidence is kept
outside the source tree. This does not complete the real CM browser gate or
assert an accepted capability.

The new candidate contains the fixed campaign protocol, public verification key,
browser harness and signed-evidence promotion checks. It is still **unaccepted**:
`desktop_web_accepted=false`, with no acceptance binding, campaign signature or
accepted image produced. The private operator signing key was not used. The
runtime's actual health response continues to omit the Desktop capability.

Local evidence is under
`.tmp/hermes-acceptance-20260908/candidate-20260908-022643/` (ignored local files).
The exact OCI archive was loaded and tested as
`agentsruntime-hermes-lite:acceptance-candidate-20260908-022643`.

| Artifact | Verified value or path beneath that directory |
| --- | --- |
| OCI index / this Docker store's image ID | `sha256:93e53d120470ea6ea6e64a2e631cb24d57aa53f2fd776af67618b4f6a655e656` |
| Tested amd64 manifest | `sha256:013ef57b85f4f2fc8f39077d61bcb36c07ce91c12d60c5f6846ca47dc36ba28f` |
| OCI config digest | `sha256:1e6219ac9516a3ff85115f35bc1729689f7e079a96db7d23312d56f7dff7f9c1` |
| Managed payload | `6a49799f0c82ba911f1b3f7dc96c15d99de71c2cbefe803ab5487b81506b33f1`; 9,793 artifact files |
| OCI archive | `candidate.oci.tar`; SHA-256 `50c364b642c7f57d5b8e845da372e63f31e40a96a3e19bb2e4398d4ed2cde769` |
| Build metadata / parameters / log | `build-metadata.json`, `build-parameters.json`, `build.log` |
| Runtime SPDX SBOM | `attestations/sbom.spdx.json`; 248 package records |
| Dashboard build SPDX SBOM | `attestations/sbom-dashboard-builder.spdx.json`; 808 package records |
| SLSA provenance / artifact hashes | `attestations/provenance.json`, `attestations/artifact-summary.json` |
| Extracted release / actual image inspection | `release.json`, `docker-image-inspect.json` |
| Runtime version and release verification | `runtime-verification.log`; passed |
| Actual suite reports and logs | `evidence/<suite>.json`, `evidence/<suite>.log`; summary `verification.json` |

Both SBOMs and provenance reference the tested amd64 manifest. The two inventories
overlap and their counts must not be combined. The build used the **uncommitted
working tree** based on `c51f573ac2fc20b6f2b0577b72ceb94d64295adc`, with a `-dirty`
revision label; that base commit does not identify unpublished changes.
`working-tree-inputs.json` records 168 source/build input hashes, rechecked unchanged
after testing. This is a hash snapshot, not an archived source checkout.

All five fixed local suites passed against this exact candidate in containers with
`--network none`: `lite_image`, `lite_provider`, `lite_agent`, `lite_non_native`,
and `desktop_rpc`. The RPC suite passed all 13 real backend cases, including
actual ticket expiry, cross-instance cookies/UIDs, streaming, interrupt, history
restore and approval/clarify. The non-native suite rejected 13 inline, 13
concurrent, 13 alias and 13 shared native dispatches with zero callbacks, while
terminal/clarify and existing work remained functional. All model responses were
from disposable local stubs.

The frozen source's Linux Python suite ran 103 tests successfully with one
platform-specific skip (Windows DACL validation); `python-linux-units.log`
contains the output. This source suite used a read-only repository mount in the
previous Linux candidate as its Python/Node executor. The new candidate itself
was tested by the five suites above. The Go full suite and four-package race
suite also passed locally. Remote GitHub Actions has not been run.

`cm_bff_browser` stopped at the runner's required configuration precheck with
exit code 1. Its report and log are retained, but the real browser suite was
**NOT RUN**: no isolated CM candidate binding/configuration exists. The schema 2
local reports have `binding_sha256:null`; they cannot be promoted or reused as
signed acceptance. Read-only discovery of the current CM identity and renderer
assets does not satisfy this gate. Production workspace upgrade/rollback, external model and
capacity acceptance also remain outstanding.

After the five local suites passed, this unchanged candidate was pushed under
the explicitly authorized, previously absent test tag
`172.16.1.12:5010/hermes-lite:hermes-acceptance-candidate-20260908-013ef57b`.
Its immutable amd64 reference for CM's isolated test environment is
`172.16.1.12:5010/hermes-lite@sha256:013ef57b85f4f2fc8f39077d61bcb36c07ce91c12d60c5f6846ca47dc36ba28f`.
`registry-172-013ef57b/registry-verification.json` records byte-for-byte equality
of the remote index, amd64 manifest, config and all three attestation blobs with
the local OCI archive; HEAD checks verified all 22 runtime layers' digests and
sizes. The same directory retains tag preflight, push output, remote manifests,
and the complete remote SBOM/provenance statements. No existing tag was replaced,
and this upload did not modify Kubernetes or ClawManager.

The parent task's read-only before/after comparison is stored in
`.tmp/hermes-acceptance-20260908/deployment-unchanged.json`: the current CM/Runtime
specifications, image identities, Pod UIDs, versions, environment names and 338
renderer asset hashes remained unchanged. No isolated acceptance namespace had
been created. Uploading this candidate does not accept or deploy it.

### Desktop Core correction: local candidate (2026-09-07)

The corrected candidate was built and tested locally as
`agentsruntime-hermes-lite:desktop-rpc-20260907-093803`. After local verification,
the exact OCI was pushed and its tested amd64 digest deployed successfully.
Its source is the uncommitted working tree based on
`c51f573ac2fc20b6f2b0577b72ceb94d64295adc`, with a `-dirty` revision label; that
base commit does not identify these uncommitted changes. The historical
`87cf168b` record below belongs to the previous candidate.

All new artifacts are preserved under
`.tmp/hermes-desktop-rpc/20260907-093803/`; the older `.tmp/hermes-lite/` evidence
was not overwritten.

| Artifact | Verified value / file under the new evidence directory |
| --- | --- |
| OCI index | `sha256:946a845b7e4f12f267f4b5b85a7711260e49961b9faa4085cfd2351fb3367240` |
| Tested linux/amd64 manifest | `sha256:c22075a7875b72df03ba4c99c80c825734789726174e3ff92f1b8c9d24247b32` |
| Image config | `sha256:0395b0fe97edf9e73f1458a627b3674d0250d3aa7537d01d2496eaa067b39427` |
| Payload hash | `b66d862e4cd89ff5a5b9f2196524c820eb2919e4987e6eb7807ff2cc193f7791` |
| OCI archive | `candidate.oci.tar`, 216,188,928 bytes; SHA-256 `e1c663fd089a02f903d2cec7a046a80233eb3c22b277a54f7fb0a68410916993` |
| Build inputs, log, metadata | `build-parameters.json`, `build.log`, `build-metadata.json` |
| Runtime / bundled npm SBOM | `attestations/sbom.spdx.json` (248 records), `attestations/sbom-dashboard-builder.spdx.json` (808 records) |
| Provenance and artifact hashes | `attestations/provenance.json`, `attestations/artifact-summary.json` |
| Managed image record / verification | `release.json`, `verification.json` |

Both SPDX attestations and the SLSA provenance were checked through the OCI
content-addressed graph and reference the tested amd64 manifest. The managed
release inventories 9,784 regular files totaling 384,394,089 bytes. Its JSON is
1,465,854 bytes, within the runtime reader's bounds. Hermes/Node versions and all
source, patch, script, ownership and payload checks passed during the build.

All five local suites passed in the exact loaded OCI candidate, using isolated
containers with `--network none`. Each has `evidence/<suite>.json` and a separately
hashed `evidence/<suite>.log`; their candidate, payload, script and runner hashes
were checked together and summarized in `verification.json`.

- `lite_image`: real password/session/ticket and HTTP/WS boundary checks, classic
  TUI fallback and retained state.
- `lite_provider`: actual saved-pool resolution with the managed instance key and
  endpoint remaining authoritative.
- `lite_agent`: real shared-agent create, readiness, replacement, isolation and
  drain/delete; the unaccepted candidate's actual health response omits capabilities.
- `lite_non_native`: real AIAgent warm/concurrent/reattach rejection preserves the
  existing work; 13 inline, 13 concurrent, 13 alias and 13 shared native dispatches
  are rejected with zero native callbacks, while terminal/clarify still work.
- `desktop_rpc`: 13 real-backend cases cover protected `.env`, two gateways and
  cross-instance cookie/UID rejection, Origin/query rejection, RPC handshake,
  streaming/persistence, clarify/approval/denial, native-call rejection, interrupt,
  warm/concurrent/cold resume, replay and real ticket expiry. Model responses come
  only from a local controlled stub; no external model service was called.

All 54 Python tests passed inside the Linux candidate with no skips; output is in
`python-linux-units.log`. The Linux Go full suite, race suites and final accepted-
release fixture checks also passed. Remote GitHub Actions has not been run.

`desktop_web_accepted` remains **false**. The `cm_bff_browser` suite was separately
executed and returned its expected unavailable-harness exit code 1; its report/log
are retained in `evidence/`. This is a recorded blocker, not a performed or failed
real browser test. Real BFF/browser, external model, production workspace
upgrade/rollback and capacity acceptance remain outstanding. No accepted layer
or production capability is claimed by these local results.

### Local verification record (2026-09-07)

Built and tested `linux/amd64` locally as `agentsruntime-hermes-lite:hermes-web`.
The source was the **uncommitted working tree** based on
`c51f573ac2fc20b6f2b0577b72ceb94d64295adc`; the image revision label adds `-dirty`.
That base commit does not identify the unpublished implementation changes. This
record describes the local candidate before publication. The same candidate was
subsequently pushed and deployed to the 172 test cluster. The GitHub Actions
workflow has not been run remotely.

| Artifact | Local evidence |
| --- | --- |
| OCI index digest | `sha256:3ed155967345699c3b69499cd974dffa29cbd1f224126211f18c6630977a7bf7` |
| Tested amd64 manifest | `sha256:87cf168b492ee1b0cb2f23f43fb3a12f5e7ca71338a984d7e9e658510d2e158b` |
| OCI archive / metadata | `.tmp/hermes-lite/candidate.oci.tar` / `.tmp/hermes-lite/build-metadata.json` |
| Runtime SPDX SBOM | `.tmp/hermes-lite/sbom.spdx.json` — 248 package records |
| Dashboard build SPDX SBOM | `.tmp/hermes-lite/sbom-dashboard-builder.spdx.json` — 808 package records, including bundled npm dependencies |
| SLSA provenance | `.tmp/hermes-lite/provenance.json` — `mode=max`; both SBOM attestations and provenance reference the tested amd64 manifest |

Artifacts and smoke logs are in the ignored local `.tmp/hermes-lite/` directory.
Both SBOMs are separate inventories and can overlap; their counts are not a
combined runtime package count.

The final source passed 15 Python lock/boundary tests and official tag, archive
and source checksum verification. The image build verified Hermes `0.21.0` and
Node `22.23.2`. All three image tests ran with `--network none`, using actual
Hermes code and disposable state:

- `smoke_lite_image.py`: password login, authenticated identity, one-time WebSocket
  tickets/ping/replay rejection, Origin/proxy rejection including explicit port
  differences, forbidden native/update APIs, fixed classic TUI, fallback HTML and
  existing state preservation. Log: `.tmp/hermes-lite/smoke-image.log`.
- `smoke_lite_provider.py`: real offline provider resolution with clean and saved
  on-disk credential pools; instance key/endpoint precedence, unchanged saved auth,
  and preserved unmanaged/flag-off behavior. Log: `.tmp/hermes-lite/smoke-provider.log`.
- `smoke_lite_agent.py`: actual shared agent asynchronous create/idempotency,
  authenticated HTTP/WebSocket readiness under a separate UID with default Docker
  capabilities, environment/permission isolation, generation replacement,
  drain/delete, metadata reclamation and workspace preservation. A local stub
  supplies control-plane reporting and Date headers. Log: `.tmp/hermes-lite/smoke-agent.log`.

The final Go 1.26.1 Linux suite passed all 14 packages; race tests passed for
gateway, Hermes runtime, agent and CLI. Built CLI dispatch checks also passed for
LLM configuration, DSH proxy help dispatch, the private listener helper and
disabled runtime mode. These are local checks. They do not establish real BFF,
browser, model-call, production workspace upgrade/rollback or capacity acceptance.

### Outstanding deployment acceptance

- Record the remaining ClawManager/Desktop revisions and approved deployment
  digest, attaching the corresponding SBOMs to the full compatibility tuple.
- Run real BFF HTTP/WS login, expiry/refresh, reconnect, streaming chat, tools,
  cancellation, model operations, session restore and classic Dashboard fallback.
- Capture browser network/DOM/storage and verify no Hermes or model credentials.
- Test NetworkPolicy, untrusted Origin, direct Pod access, cross-instance access,
  forged tickets, forbidden native APIs and path traversal against the real deployment.
- Rehearse an old workspace upgrade and rollback without removing workspace data.
- Exercise process crash, Pod/agent restart, drain and port reclamation under load;
  measure at least the configured capacity and review resource regression.
- Keep ordinary Lite behind ClawManager's feature flag. Team, Pro and native Desktop
  APIs remain outside this candidate's supported scope; no Desktop capability is
  advertised through an invented runtime registration field.

Rollback switches back to the prior accepted image digest and turns off the
ClawManager Desktop Web feature flag. Preserve the workspace, sessions, config
backups and schema marker; do not delete them to make an incompatible image start.
