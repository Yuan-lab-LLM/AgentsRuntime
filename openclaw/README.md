# ClawManager OpenClaw Image

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

**Prefer `Dockerfile.openclaw` for a one-shot build.** If you need to install components manually inside the WebTop desktop and then `docker commit`, see **Advanced: manual flow** below.


## Project overview

In ClawManager batch scenarios, per-container manual setup does not scale. This project addresses:

* **Pre-installed runtime**: Node.js and the latest OpenClaw CLI, ready to use.
* **Agent-driven bootstrap**: `openclaw-agent` (Go) runs as root at container start, seeds `/config/.openclaw` from `/defaults/.openclaw`, reconciles channel plugins, fixes ownership, and drops privileges to `abc` before launching OpenClaw.
* **Dynamic injection**: Environment variables update `openclaw.json` without editing files in the desktop session.

---

## Quick start (recommended)

The image is based on `lscr.io/linuxserver/webtop:ubuntu-xfce`. The Dockerfile installs Node.js and global OpenClaw, seeds `/defaults/.openclaw`, and ships the `openclaw-agent` binary as an s6 service. On every container start the agent performs all bootstrap work (defaults sync, extensions directory, XFCE autostart, config normalization, channel reconciliation) and then drops privileges to `abc` before launching `openclaw gateway run`. Runtime config lives under **`/config/.openclaw`**, not `~/.openclaw`. Look for `openclaw-agent` lines in the container logs after startup.

### Build

**Bash**

```
docker build -f Dockerfile.openclaw -t openclaw:local .
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

The workflow [`.github/workflows/docker-ghcr.yml`](.github/workflows/docker-ghcr.yml) builds `Dockerfile.openclaw` on push to the default branch (`main` / `master`) or on `v*` tags, and pushes to **GitHub Container Registry** so you do not need a local `docker build` for releases.

**Short checklist**

1. Push the repo to GitHub and confirm **Build and push to GHCR** succeeds under **Actions**.
2. Find the package under **Packages**; the image is usually `ghcr.io/<user>/<repo>`.
3. For private packages, run `docker login ghcr.io` first; set the package to **Public** if you want anonymous `docker pull`.

**Bash**

```
docker pull ghcr.io/<github_user>/<repo>:latest

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
  ghcr.io/<github_user>/<repo>:latest
```

Pushing tags like `v1.0.0` also publishes semver tags per the workflow metadata rules.

---

## Advanced: manual flow (`docker commit`)

Use this when you must install extra tooling inside WebTop before saving an image. It is an alternative to `Dockerfile.openclaw`.

### Install software

Open `https://<IP>:3001`, then in a terminal:

**Bash**

```
curl -fsSL https://deb.nodesource.com/setup_current.x | sudo -E bash -
sudo apt-get install -y nodejs

npm config set registry https://registry.npmmirror.com
sudo npm install -g openclaw@latest
```

### Init script and cleanup

1. **Seed defaults**: `cp -rp /config/.openclaw /defaults/`.
2. **Install agent**: copy the compiled `openclaw-agent` binary into `/usr/local/bin/` and install `scripts/openclaw-agent-run` / `openclaw-agent-finish` under `/etc/services.d/openclaw-agent/`. The agent performs the `/defaults` → `/config` sync and env-based edits on every start.
3. **Clean before image save**: `rm -rf /config/.openclaw`. If this step is skipped, new containers may not run first-boot init as expected.

### Save image

**Bash**

```
docker commit webtop-running openclaw:v1.0
```

---

## Notes

* **Permissions**: the agent runs `chown -R abc:abc` on `/config/.openclaw` and `/config/.config/autostart` before dropping to `abc`, so the default user can read/write persisted config.
* **Docker Compose**: point `image` at your built tag, or use `build` with `dockerfile: Dockerfile.openclaw`. Do not rely on the stock `webtop` image alone; it will not include this repo’s templates and agent service.
* **Standalone WebTop**: if you do not use ClawManager batch features, you can skip the ClawManager-specific steps in the advanced flow.

---

## Links

* [ClawManager - The friendliest way to manage Al agents](https://github.com/Yuan-lab-LLM/ClawManager)
