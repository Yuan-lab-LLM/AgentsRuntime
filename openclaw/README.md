# ClawManager OpenClaw Image

> Maintainers adapting channel plugins for the multi-instance Lite runtime
> should also read the [OpenClaw Lite Channel Adapter Guide](../clawmanager-agent/docs/openclaw-lite-channel-adaptation.md).

<p align="center">
  <img src="docs//assets/openclaw_logo.jpg" alt="ClawManager" width="100%" />
</p>

<p align="center">
  <strong>Languages:</strong>
  <a href="./README.md">English</a> |
  <a href="./README.zh-CN.md">中文</a>
</p>

This guide helps you build an **OpenClaw** base image for the **ClawManager** control plane, with **automated config injection** (API Key, Base URL, etc.) and **persistent directory layout** for multi-tenant scenarios.

The image now also includes an **OpenClaw Agent** service inside the container. It auto-registers to ClawManager, sends heartbeats, polls commands, manages the OpenClaw process, and exposes a local Gin-based health endpoint.

**Use `openclaw/Dockerfile.openclaw` from the repository root for release builds.** It pins OpenClaw **2026.10.2** and Node **24.21.0** through [the version lock](openclaw102.lock.json), checks package integrity, and verifies the installed version. For extra tools, see **Reproducible customization** below.


## Project overview

In ClawManager batch scenarios, per-container manual setup does not scale. This project addresses:

* **Pinned runtime**: Node.js 24.21.0, OpenClaw 2026.10.2, and locked managed channel versions, rather than an unbounded `openclaw@latest` installation.
* **Agent-driven bootstrap**: in standalone/Pro mode, `openclaw-agent` (Go) seeds `/config/.openclaw` from `/defaults/.openclaw`, reconciles channels and ownership, and launches OpenClaw as `abc`. Pooled Lite uses the assigned per-instance HOME and Linux identity instead.
* **Dynamic injection**: Environment variables update `openclaw.json` without editing files in the desktop session.

---

## Quick start (recommended)

The canonical image uses the digest-pinned `lscr.io/linuxserver/webtop:ubuntu-kde-version-cc2e18a4` base, installs the locked Node.js/OpenClaw packages, seeds `/defaults/.openclaw`, and ships the agent services. Standalone/Pro config lives under **`/config/.openclaw`**; pooled Lite gateways use their ClawManager-assigned per-instance HOME and workspace, not a shared `/config` for all users. Check agent logs and actual instance health after startup.

Both `openclaw` and `openclaw-lite` use this Dockerfile. Team UI and file editing require a compatible ClawManager App; Runtime adapters provide member execution and delivery. Updating the image alone does not update the UI or safely migrate existing state. Use ClawManager's data-safe upgrade workflow and validate the actual source-to-target path. `openclaw-shell` is a separate build and is not upgraded by this pin.

### Build

**Bash**

Run this from the repository root so Docker can include the shared
`clawmanager-agent/` component.

```
docker build -f openclaw/Dockerfile.openclaw -t openclaw:local .
```

### Run

> **Set shared memory**: always pass `--shm-size="1gb"` (at least 1GB) when running WebTop, or the browser/desktop stack may crash or behave oddly.

**Bash**

```
docker run -d \
  --name=webtop-openclaw \
  --shm-size="1gb" \
  --restart unless-stopped \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=Asia/Shanghai \
  -e CLAWMANAGER_LLM_BASE_URL=https://your-gateway/v1 \
  -e CLAWMANAGER_LLM_API_KEY=your-sk-key \
  -e CLAWMANAGER_LLM_MODEL=gpt-4o \
  -p 3000:3000 \
  -p 3001:3001 \
  openclaw:local
```

Adjust ports and placeholders as needed.

---

## Environment variables

Set these in ClawManager or `docker run` to inject into `openclaw.json`:

| Variable                     | Config path                              | Purpose                                                                                   |
| ---------------------------- | ---------------------------------------- | ----------------------------------------------------------------------------------------- |
| `CLAWMANAGER_LLM_BASE_URL` | `models.providers.auto.baseUrl`        | Gateway or upstream base URL; OpenClaw uses the injected value directly                    |
| `CLAWMANAGER_LLM_API_KEY`  | `models.providers.auto.apiKey`         | Model API key or ClawManager-issued gateway token                                         |
| `CLAWMANAGER_LLM_MODEL`    | `agents.defaults.model.primary` / `agents.defaults.models` | Model id replacement; supports a single id or a JSON array; written as `auto/<model>` refs so embedded agent runs reuse the managed gateway provider |
| `CLAWMANAGER_OPENCLAW_CHANNELS_JSON` | `channels` (merge)                     | JSON object with one or more channel keys (`feishu`, `slack`, …); shallow-merge into `channels`; invalid JSON aborts startup |
| `OPENCLAW_AGENT_INSTANCE_ID` | agent bootstrap                         | Required. Unique instance id used during `/api/v1/agent/register` |
| `OPENCLAW_AGENT_BOOTSTRAP_TOKEN` | agent bootstrap                      | Required. Bootstrap token for agent registration |
| `OPENCLAW_AGENT_CONTROL_PLANE_BASE_URL` | agent bootstrap             | Required. ClawManager base URL |
| `OPENCLAW_AGENT_INITIAL_CONFIG_REVISION_ID` | agent bootstrap      | Optional initial revision id |
| `OPENCLAW_AGENT_OPENCLAW_COMMAND` | process management              | Optional. Defaults to `openclaw gateway run` |
| `OPENCLAW_AGENT_OPENCLAW_DOCTOR_COMMAND` | startup repair          | Optional. Defaults to `openclaw doctor --fix` |
| `OPENCLAW_AGENT_OPENCLAW_DOCTOR_POLICY` | startup repair          | Optional. `auto` (default), `always`, or `never`; `auto` does not run doctor before the first launch, and only repairs when the gateway fails to start, exits during startup, or health does not come up |
| `OPENCLAW_AGENT_OPENCLAW_STARTUP_HEALTH_TIMEOUT` | process management | Optional. Defaults to `90s`; wait time before auto doctor when gateway health is not ready |
| `OPENCLAW_AGENT_STARTUP_NOTIFICATION_MESSAGE` | desktop notification | Optional. Defaults to `正在启动龙虾` |
| `OPENCLAW_AGENT_STARTUP_REPAIR_NOTIFICATION_MESSAGE` | desktop notification | Optional. Defaults to `正在修复启动环境，可能需要 1-2 分钟` |

The agent default config is stored at `/etc/openclaw-agent/config.yaml`, seeded from `/defaults/openclaw-agent/config.yaml`, and the local health/debug server listens on `:18080` by default.

---

## GitHub Actions and GHCR (optional)

The workflow [`.github/workflows/docker-ghcr.yml`](../.github/workflows/docker-ghcr.yml) builds after matching source changes reach `main`, or after a manual run on the default branch. Automatic runs require `main` to be the default branch too. Feature-branch pushes, `v*` tags, and README-only changes do not trigger publication. The current filter also excludes `plugins/**`; run the workflow manually after a plugin-only merge.

**Short checklist**

1. Merge into `main` and confirm **Build and push runtime images**, including both architectures and the final manifest publish job, succeeds under **Actions**.
2. Default package paths are `ghcr.io/<owner>/<repo>/openclaw` and `ghcr.io/<owner>/<repo>/openclaw-lite`, with lowercase owner/repository names. Fork publications do not update upstream packages.
3. For private packages, run `docker login ghcr.io` first; set the package to **Public** if you want anonymous `docker pull`.

**Bash**

```
docker pull ghcr.io/<owner>/<repo>/openclaw:latest

docker run -d \
  --name=webtop-openclaw \
  --shm-size="1gb" \
  --restart unless-stopped \
  -e PUID=1000 -e PGID=1000 -e TZ=Asia/Shanghai \
  -e CLAWMANAGER_LLM_BASE_URL=https://your-gateway/v1 \
  -e CLAWMANAGER_LLM_API_KEY=your-sk-key \
  -e CLAWMANAGER_LLM_MODEL=gpt-4o \
  -e CLAWMANAGER_OPENCLAW_CHANNELS_JSON='{"feishu":{"enabled":true,"accounts":{"main":{"appId":"cli_xxx","appSecret":"your-secret"}}}}' \
  -p 3000:3000 -p 3001:3001 \
  ghcr.io/<owner>/<repo>/openclaw:latest
```

Successful publication creates `latest` and `vYYYY.M.D` (Asia/Shanghai). These tags can move; record and deploy the verified `@sha256` digest. A new CI build need not have the same digest as an earlier test image, even when both use OpenClaw 2026.10.2.

Inspect the installed core without starting a Gateway or mounting user data:

```bash
docker run --rm --network none --entrypoint node <image>@sha256:<verified-digest> \
  -p 'require("/usr/local/lib/node_modules/openclaw/package.json").version'
```

---

## Reproducible customization

Add tools in a tracked Dockerfile derived from a verified runtime digest, then rebuild and test the resulting image. Do not replace Node or OpenClaw with floating versions, package a user's HOME or credentials with `docker commit`, or delete persisted state to force initialization. The historical interactive install/commit recipe is not a release or migration procedure.

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
# Add explicitly versioned tools here; preserve the runtime and agent services.
```

Overlay Dockerfiles update selected components only and inherit the base core version. Choose the base by verified digest and repeat the relevant compatibility tests; an overlay is not evidence of a 10.2 core upgrade.

---

## Notes

* **Permissions**: preserve agent-assigned ownership and identity. Standalone/Pro and pooled Lite have different HOME and UID boundaries; do not recursively chown a shared Lite workspace to `abc`.
* **Docker Compose**: point `image` at your built tag, or use `build` from the repository root with `context: .` and `dockerfile: openclaw/Dockerfile.openclaw`. Do not rely on the stock `webtop` image alone; it will not include this repo’s templates and agent services.
* **Standalone WebTop**: desktop startup alone does not validate pooled Lite isolation, Team delivery, or data migration; test the managed mode separately.

---

## Links

* [ClawManager - The friendliest way to manage Al agents](https://github.com/Yuan-lab-LLM/ClawManager)
