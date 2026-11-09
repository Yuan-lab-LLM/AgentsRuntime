# Agents Runtime Images

This repository contains Docker image definitions for the runtimes used by the ClawManager Agents project.

The repository currently documents these runtime images:

- `hermes`
- `hermes-lite`
- `openclaw`
- `openclaw-lite`
- `openclaw-shell`
- `opencode`
- `opencode-lite`
- `deepseek-harness`
- `deepseek-harness-lite`
- `windows-vm`

## Repository layout

- `hermes/`: Hermes runtime image, built from the repository root so it can include the shared `clawmanager-agent/`
- `hermes-lite`: headless Hermes Desktop Web candidate, built with `hermes/Dockerfile.lite`; see [release gates](hermes/HERMES-LITE-RELEASE.md) and [operations](hermes/HERMES-LITE-OPERATIONS.md)
- `openclaw/`: OpenClaw runtime image, built from the repository root so it can include the shared `clawmanager-agent/`
- `openclaw-lite`: lite OpenClaw runtime image, built from the repository root with `openclaw/Dockerfile.openclaw`
- `openclaw-shell/`: Alpine-based OpenClaw shell runtime image, built from the repository root so it can reuse the OpenClaw agent implementation under `openclaw/`
- `opencode/`: OpenCode Lite/Pro runtime image (official `opencode web` + shared `clawmanager-agent/`)
- `deepseek-harness/`: DeepSeek Harness Lite and Pro runtime images plus their managed configuration and startup scripts
- `windows-vm/`: Windows VM runtime wrapper around `dockurr/windows`, built from the repository root so it can include the shared `clawmanager-agent/`
- `clawmanager-agent/`: shared managed-runtime agent used by runtime images that need ClawManager runtime gateway control

Before adding or upgrading a channel plugin in OpenClaw Lite, read the
[OpenClaw Lite Channel Adapter Guide](clawmanager-agent/docs/openclaw-lite-channel-adaptation.md).

## OpenClaw version and Team compatibility

The canonical `openclaw` (Pro) and `openclaw-lite` builds both use
`openclaw/Dockerfile.openclaw` and pin OpenClaw **2026.10.2** with Node
**24.21.0**. The [version lock](openclaw/openclaw102.lock.json) records core
package integrity and managed channel versions; the build verifies both the
download and the installed version. This does not upgrade `openclaw-shell`.
Overlay Dockerfiles inherit their base image's core version and do not replace
the canonical release build.

Runtime Team adapters support compatible ClawManager Leader-mediated and
single-member delivery contracts. Continuous-collaboration chat, functional
avatars, the shared-file editor, and the `@member` selector belong to the
**ClawManager App**. Use compatible App and Runtime versions; publishing a
Runtime image neither updates the UI nor migrates or restarts existing instances.
Data-format upgrades require the validated ClawManager migration workflow, not
a direct image replacement.

## GitHub Actions and image tags

The [image workflow](.github/workflows/docker-ghcr.yml) runs on matching source
changes pushed to `main`, or manually on the repository's default branch. The
branch check also requires the selected branch to be the default branch.
Feature-branch pushes, `v*` tags, and README-only changes do not publish images.
The current path filter excludes `plugins/**`; after a plugin-only change is
merged, run the workflow manually.

Unless a manual run specifies `image_namespace`, images use the lowercased
repository namespace:

```text
ghcr.io/<owner>/<repo>/openclaw:latest
ghcr.io/<owner>/<repo>/openclaw-lite:latest
```

For example, `Stark-Han/AgentsRuntime` publishes under
`ghcr.io/stark-han/agentsruntime/`; a fork does not update upstream packages.
Successful amd64 and arm64 builds are combined into manifests tagged `latest`
and `vYYYY.M.D` (Asia/Shanghai). Check the final publish job and manifest, not just
a successful architecture build. Tags can move: deploy a verified `@sha256`
digest for repeatable upgrades. A failed or cancelled run is not a new release.

## Manual builds

You can build each runtime image directly with Docker from the repository root.

### Hermes

The Pro image pins Hermes Agent `0.21.0` (`v2026.8.31`) and opens the native
Hermes Desktop application inside Webtop. The former terminal and Web Dashboard
autostart paths are not included in the Pro image.

```bash
docker build \
  -f hermes/Dockerfile \
  -t hermes:local \
  .
```

### Hermes Lite

```bash
docker build \
  -f hermes/Dockerfile.lite \
  -t hermes-lite:local \
  .
```

### OpenClaw

```bash
docker build \
  -f openclaw/Dockerfile.openclaw \
  -t openclaw:local \
  .
```

### OpenClaw Lite

```bash
docker build \
  -f openclaw/Dockerfile.openclaw \
  -t openclaw-lite:local \
  .
```

### OpenClaw Shell

```bash
docker build \
  -f openclaw-shell/Dockerfile \
  -t openclaw-shell:local \
  .
```

This image does not include Webtop or a virtual desktop. It uses `/config` as the persistent directory, runs `openclaw-agent` on container start, and reports `runtime_type=openclaw-shell` to ClawManager when `CLAWMANAGER_AGENT_ENABLED=true`.

### OpenCode

```bash
docker build \
  -f opencode/Dockerfile \
  -t opencode:local \
  .
```

### OpenCode Lite

```bash
docker build \
  -f opencode/Dockerfile \
  -t opencode-lite:local \
  .
```

Lite and Pro share the same Dockerfile. Tag as `opencode-lite` for Runtime Pod Agent V2 (`opencode web` via CreateGateway) and `opencode` for dedicated Webtop Pro. OpenCode is pinned via `OPENCODE_VERSION` (default `1.18.13`).

### DeepSeek Harness Lite

```bash
docker build \
  -f deepseek-harness/Dockerfile.lite \
  -t deepseek-harness-lite:local \
  .
```

This is a pooled headless runtime. The shared agent starts one isolated
DeepSeek Harness Web gateway per assigned instance and persists its state in
the instance workspace under `home/.dsh`.

### DeepSeek Harness Pro

```bash
docker build \
  -f deepseek-harness/Dockerfile.pro \
  -t deepseek-harness:local \
  .
```

This is a dedicated Webtop runtime. DeepSeek Harness listens on loopback port
`3080`, while the desktop remains exposed through the inherited Webtop service.

### Windows VM

```bash
docker build \
  -f windows-vm/Dockerfile \
  -t windows-vm:local \
  .
```

## Manual multi-architecture builds

If you want to build multi-architecture images manually, use Docker Buildx.

### Hermes

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f hermes/Dockerfile \
  -t <registry>/hermes:latest \
  --push \
  .
```

### Hermes Lite

The unified `docker-ghcr.yml` image matrix builds and publishes Hermes Lite with
`hermes/Dockerfile.lite`, alongside the other Pro and Lite runtime images.
Complete the [Hermes Lite release gates](hermes/HERMES-LITE-RELEASE.md) before
deploying a tested immutable digest. Validate each target architecture separately.

### OpenClaw

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f openclaw/Dockerfile.openclaw \
  -t <registry>/openclaw:latest \
  --push \
  .
```

### OpenClaw Lite

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f openclaw/Dockerfile.openclaw \
  -t <registry>/openclaw-lite:latest \
  --push \
  .
```

### OpenClaw Shell

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f openclaw-shell/Dockerfile \
  -t <registry>/openclaw-shell:latest \
  --push \
  .
```

### DeepSeek Harness Lite

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deepseek-harness/Dockerfile.lite \
  -t <registry>/deepseek-harness-lite:latest \
  --push \
  .
```

### DeepSeek Harness Pro

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deepseek-harness/Dockerfile.pro \
  -t <registry>/deepseek-harness:latest \
  --push \
  .
```

The supported architectures are:

- `linux/amd64`
- `linux/arm64`

This publishes a single multi-arch image manifest, so both architectures are available under the same tag.
