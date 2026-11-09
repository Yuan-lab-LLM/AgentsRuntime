# Hermes Lite v0.22.5

This update targets newly provisioned Hermes Lite instances. Runtime and ClawManager must be deployed as a compatible pair. It does not introduce an old-workspace migration API or batch migration tooling. Existing 0.21.0 workspaces must not be pointed at the new image.

The pinned source is `NousResearch/hermes-agent` tag `v0.22.5`, commit `bf0203a794a07aac3ff764972ab9087baa4e4f4e`. Source archives, patch files and release artifacts remain integrity checked. Release compatibility does not imply signed end-to-end acceptance.

## GitHub architecture integration

- The runtime implementation stays in `hermes/lite`; its executable is `hermes/cmd/hermes-lite-agent`, composed with the public shared `clawmanager-agent/gateway` module.
- NFS credential-scoped preparation, isolated slots, automatic recovery, idle stop, managed credentials and Provider grouping remain in their existing integration paths.
- Python 3.14, the upstream sealed package-manager environment and headless TUI build replace the 0.21 build recipe. Startup instrumentation, bounded skill traversal and read-only probes for already initialized stores reduce repeat startup work.
- Version-specific Team completion and external-skill deduplication adapters preserve GitHub behavior in the new split tool round and skill-sync state. They run after the version-locked startup patch. The Team supervisor and release inventory are retained.
- ClawManager keeps its root-level `hermes-desktop-web` package and adapts the browser bridge and bidirectional RPC to the new protocol. Approval responses remain socket/session scoped and preserve GitHub's restricted approval projection. Verified renderer caches are replaced atomically to remove retired upstream files.

## Scope and provenance

Adapted from GitLab runtime commit `061e329` and ClawManager commit `4223b774`. The initial sync excluded migration commits and uncommitted session-schema/FTS/model-catalog fixes. The 2026-11-09 picker follow-up incorporates the reviewed model-catalog cold-load patch and its tests; legacy session-schema/FTS migration remains excluded. Pro/Webtop is outside this Lite upgrade.

Before publishing, build the runtime image, run its release verification and perform a paired new-instance smoke test (create, model selection, chat, restart and Team where enabled). Do not reuse previous release signatures or claim deployment acceptance from local unit tests.

## Local validation (2026-11-09)

- ClawManager Hermes service tests passed on Linux. Renderer: 36 tests, TypeScript check, dependency-closure audit and production build passed.
- Runtime Hermes module and shared Gateway Go tests passed on Linux.
- Python boundary/supply-chain/adapter suite: 140 tests, 131 passed and 9 skipped because they require an installed Hermes application environment.
- Docker `patched-source` target built successfully from the verified upstream archive, including the v0.22.5 Team completion and skill-deduplication adapters.
- These were the initial source-level checks. Subsequent full-image and isolated cluster verification is recorded below; it does not constitute signed release promotion.

## Rootless image validation (2026-11-09)

A full-image Kubernetes test exposed a packaging permission issue: the sealed PM metadata `pm-runtime/pm-runtime.json` was readable only by root. The NFS coordinator runs as UID 10001, so its Go artifact verifier omitted the Desktop capability even though the native gateways could start. The Dockerfile now gives this non-secret, root-owned metadata mode 0644 before release assembly. No credential or workspace permissions are relaxed.

The corrected `hermes-lite:v0.22.5-ltt-20261109-r2` image passed `TestDesktopCapabilityActualImage` as UID/GID 10001, the installed-application `smoke_lite_image.py`, and `smoke_lite_desktop_rpc.py` with networking disabled and a local model stub. These are functional checks, not signed release promotion or live-provider acceptance.

### Model picker follow-up

The Web picker accepts only the local `default`/`current` profile labels and removes them before forwarding to the authenticated instance. Ordinary model catalogue reads use background manifest and optional models.dev metadata refresh and the upstream 1.5-second custom-provider discovery budget; explicit Refresh Models retains full discovery. Configured/cached model rows remain fallback when discovery times out. The patch, upstream files and regression tests are included in the pinned release inputs.

## Paired cluster verification (2026-11-09)

The final runtime image and compatible ClawManager were tested in a dedicated Kubernetes namespace: four Hermes instances across two Pods with four slots each, using a native NFS server and `nfs-rootless-v1`. Nonroot capability and installed-application smoke checks passed. Three configured providers and 24 models were returned in 42–252 ms on initial reads after restart; manual refresh also passed.

Authenticated HTTPS/BFF WebSocket checks covered advertised provider aliases, session creation, real model responses and streaming, reconnect/resume, event replay and persistent conversation context. NFS probe files survived slot container recreation and cross-Pod migration. The paired ClawManager fix waits for kubelet termination evidence before target launch and supports retry of a failed source stop; actual retry and migration passed while neighboring instances remained running.

Final targeted runtime verification included 36 release/supply-chain tests and ten catalogue/metadata/budget tests, in addition to the earlier Go, Python and full-image checks. This is functional test evidence, not signed promotion. Actual browser interaction, cross-node scheduling, Longhorn RWX, Team end-to-end acceptance and migration of old 0.21.0 workspaces are not established by these tests.

### Publish preflight follow-up

The first post-merge publish run stopped before matrix generation: the shared static contract expected the complete `hermes_cli/tui_dist/entry.js` path in the Dockerfile, while the new build copied the containing directory. The final image now explicitly requires this entry file to be nonempty. Both shared Lite contract and entrypoint checks also run on pull requests, without registry credentials or publishing images. A passing preflight does not establish that every architecture build or registry push has completed.
