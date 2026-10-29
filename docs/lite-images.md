# Building and checking the four Lite images

Run all commands from the `AgentsRuntime` repository root. Each runtime has one
official Lite build file, `<runtime>/Dockerfile.lite`, and compiles its own
`cmd/<runtime>-lite-agent` into `/usr/local/bin/clawmanager-agent`. The sibling
`clawmanager-agent` module supplies the common core. The GHCR manual and push
matrices use the repository root context and these four files.

| Runtime | Locked release | Final image ancestry | Preserved payload |
| --- | --- | --- | --- |
| OpenClaw | 2026.10.2, Node 24.21.0 | Digest-pinned Node Debian slim | Managed proxy, publication/upgrade/memory/browser patches; pinned DingTalk, WeCom and Feishu; Redis Team 0.3.1; headless Chromium; immutable default plugin payload |
| Hermes | 0.21.0, tag v2026.8.31, commit 29112bef099274229cadff79cdff7bf7b99c4b77 | Digest-pinned Python 3.13 Debian slim; Node 22.23.2 session worker | Locked source and patch verification, Web/RPC, headless session worker, Redis Team, skills, tool executor and signed release acceptance machinery |
| OpenCode | 1.18.13 | Digest-pinned Node 22.23.2 Debian slim | Official native Web/PTY server, per-instance config/project handling, Git/search and development tools; Node/npm support user projects |
| DeepSeek Harness | 0.1.2-alpha.3, pnpm 11.25.0 | Existing digest-pinned Node 22 Debian slim | Managed Cordis patch, authenticated Web proxy, plugin installs, Team, resource preparation and image-owned proxy binary |

The final images contain `tini` for reaping, `bubblewrap` and `util-linux` for
the shared Linux sandbox, and Bash/Git/SSH/ripgrep/curl for user tool tasks and
operations. Compilers and Python remain available for user projects and native
plugin/package builds. OpenClaw's Chromium is the existing headless gateway
browser; no desktop server or desktop autostart services are copied. Pro images
retain their own build files and services.

`RUNTIME_AGENT_ROLE=pool` requires the existing control and report credentials.
`RUNTIME_AGENT_ROLE=slot` requires the trusted local control credential and does
not require backend reporting credentials. The wrappers reject unknown roles
and Pro instance mode. An absent role retains the prior legacy startup behavior
(Hermes requires both credentials; other runtimes idle when both are absent).

## Update only the Lite manager on an existing native image

OpenClaw, Hermes and OpenCode provide `<runtime>/Dockerfile.lite-agent-overlay`
for Linux amd64 maintenance builds. Supply the exact deployed immutable image
as `BASE_IMAGE` and use this repository as the build context. The builder
compiles the current `cmd/<runtime>-lite-agent` with the shared core. The final
stage inherits the native application, environment, entrypoint and release
labels from that base. Hermes also runs the base image's release verifier to
regenerate the manager hash; its new payload starts with
`desktop_web_accepted=false` and retains the normal capability verification.

Build and import on the deployment build host, then use the runtime rollout
API. OpenClaw uses its data-safe preflight and confirmation flow. Before moving
an instance off an old image whose `/tmp` is tmpfs, preserve the final files
after confirmed stop and import under the existing stopped writer lease. Verify
the actual business process `/tmp` mount and cross-Pod file contents after the
rollout. Building an image alone does not update a running pool.

DeepSeek Harness's official `Dockerfile.lite` also patches its pinned native
sandbox profile so workspace-write commands bind the outer persistent `/tmp`.

## Offline checks

```sh
python3 tests/lite/check_build_contract.py
python3 tests/lite/check_entrypoints.py
python3 hermes/scripts/verify_lite_release.py \
  --lock hermes/hermes-lite.lock.json \
  --patch-root hermes/patches/hermes-agent \
  --test-root hermes/tests --acceptance-root hermes/acceptance
python3 -m unittest discover -s hermes/tests -p test_team_completion_stop.py
bash -n tests/lite/smoke_image.sh
```

The contract check resolves final-stage ancestry, rejects Pro/Webtop ancestors
and desktop service payloads, verifies root-context COPY inputs and required
runtime payload/version locks, checks RUN shell syntax, and checks every Lite
publish mapping. The wrapper check runs each real entry script with only the
final `exec` intercepted and verifies credential/role failures without starting
a daemon. These checks do not build an OCI image.

The Hermes lock's `apply_team_completion_stop.py` digest was corrected from
`442d156b27f3a7a2805f75f8a87acb503e6d0f70c49531ec7c46ce63afaac979` to
`1acebcd53a5aef53ee20e3c464e72acef6461a7949b6d5f4d935083b1f124fa0`.
The latter matches the existing tracked patch from commit `75da395`, which
extended durable receipts to submitted/deferred while keeping ClawManager's
acknowledgement authoritative. No patch body or upstream release lock changed.

## Docker build and runtime gates

Docker builds and container smoke tests require a machine with a working Linux
Docker engine and registry access. They were **not executed in the development
sandbox**, where access to the Docker socket is denied.

Build each image for the host architecture:

```sh
docker buildx build --load --platform linux/amd64 -f openclaw/Dockerfile.lite -t cm/openclaw-lite:slots .
docker buildx build --load --platform linux/amd64 -f hermes/Dockerfile.lite -t cm/hermes-lite:slots .
docker buildx build --load --platform linux/amd64 -f opencode/Dockerfile.lite -t cm/opencode-lite:slots .
docker buildx build --load --platform linux/amd64 -f deepseek-harness/Dockerfile.lite -t cm/deepseek-harness-lite:slots .
```

Repeat with `--platform linux/arm64` on an ARM64 runner; the publish workflow
builds both architectures independently. To inspect each image's required
tools and run the supplied runtime-specific smoke:

```sh
docker run --rm -i --shm-size=1g --entrypoint /bin/bash cm/openclaw-lite:slots -s -- openclaw < tests/lite/smoke_image.sh
docker run --rm -i --entrypoint /bin/bash cm/hermes-lite:slots -s -- hermes < tests/lite/smoke_image.sh
docker run --rm -i --entrypoint /bin/bash cm/opencode-lite:slots -s -- opencode < tests/lite/smoke_image.sh
docker run --rm -i --entrypoint /bin/bash cm/deepseek-harness-lite:slots -s -- deepseek-harness < tests/lite/smoke_image.sh
```

OpenClaw checks installed patches, channel dependency closure and an actual
headless browser process. Hermes checks its release inventory, tool boundary,
login/WebSocket protocol and session worker. OpenCode starts its actual native
server with disposable authenticated state. DeepSeek checks its locked package,
pnpm, resource preparation and assembled proxy command. The DeepSeek check does
not make a model request or prove Team delivery; those require full managed
runtime acceptance. No smoke mounts the Docker socket or host credentials.

Also run the common sandbox's Linux acceptance gate under the intended Pod
security policy, including any approved namespace capabilities:

The gate runs as the assigned nonroot UID/GID, and a separate root supervisor
mode checks cross-UID stop and listener ownership. Final slim images do not need
Go: cross-compile the test binary first and provide it read-only alongside the
script. For example, build the amd64 gate artifact on the development machine:

```sh
mkdir -p /private/tmp/cm-lite-acceptance
cd clawmanager-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /private/tmp/cm-lite-acceptance/sandbox.test ./sandbox
cp sandbox/acceptance-linux.sh /private/tmp/cm-lite-acceptance/
```

Inside the actual Linux image/security context, with these files mounted at
`/acceptance`, execute each mode separately:

```sh
# As the intended nonroot gateway UID/GID:
CM_SANDBOX_TEST_BINARY=/acceptance/sandbox.test /acceptance/acceptance-linux.sh
# As the actual trusted supervisor with its configured capability set:
CM_SANDBOX_TEST_BINARY=/acceptance/sandbox.test /acceptance/acceptance-linux.sh --supervisor
```

Use an arm64 test binary on arm64. The supervisor gate requires the same
SETUID/SETGID/CHOWN/DAC_OVERRIDE/FOWNER/KILL/SYS_PTRACE capabilities configured
in the generated Pod; gateways receive none. Do not add privileges to conceal a
failed gate. On 2026-10-17 both modes passed on node12 with direct Docker and
explicit systempaths=unconfined, using the final shared sandbox source. The
actual Kubernetes gate is still blocked by the customized v1.20.5 dockershim
empty-list handling; a matching-source kubelet repair and real Pod rerun are
required before production conversion. All four linux/amd64 images were built,
pushed and smoke-tested on node12; no OCI image was built on the local Mac.

The image checks do not establish sandbox isolation, cross-node migration,
real model/tool calls, Team delivery, storage fencing, resource density or
upgrade acceptance. Those remain separate deployment release gates. Record
both architectures' image sizes and per-runtime idle/load CPU, memory, startup
and migration timings after the functional/security gates pass; a slim base
alone is not evidence of lower operating memory.
