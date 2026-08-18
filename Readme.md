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
- `codex`
- `claude-code`
- `windows-vm`

## Repository layout

- `hermes/`: Hermes runtime image, built from the repository root so it can include the shared `clawmanager-agent/`
- `hermes-lite`: lite Hermes runtime image, built from the repository root with the Hermes Dockerfile
- `openclaw/`: OpenClaw runtime image, built from the repository root so it can include the shared `clawmanager-agent/`
- `openclaw-lite`: lite OpenClaw runtime image, built from the repository root with `openclaw/Dockerfile.openclaw`
- `openclaw-shell/`: Alpine-based OpenClaw shell runtime image, built from the repository root so it can reuse the OpenClaw agent implementation under `openclaw/`
- `opencode/`: OpenCode Lite/Pro runtime image (official `opencode web` + shared `clawmanager-agent/`)
- `codex/`: Codex Pro desktop runtime (official Codex CLI + shared `clawmanager-agent/`)
- `claude-code/`: Claude Code Pro desktop runtime (official Claude Code CLI + shared `clawmanager-agent/`)
- `windows-vm/`: Windows VM runtime wrapper around `dockurr/windows`, built from the repository root so it can include the shared `clawmanager-agent/`
- `clawmanager-agent/`: shared managed-runtime agent used by runtime images that need ClawManager runtime gateway control

Before adding or upgrading a channel plugin in OpenClaw Lite, read the
[OpenClaw Lite Channel Adapter Guide](clawmanager-agent/docs/openclaw-lite-channel-adaptation.md).

## Manual builds

You can build each runtime image directly with Docker from the repository root.

### Hermes

```bash
docker build \
  -f hermes/Dockerfile \
  -t hermes:local \
  .
```

### Hermes Lite

```bash
docker build \
  -f hermes/Dockerfile \
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

### Codex

```bash
docker build -f codex/Dockerfile -t codex:local .
```

### Claude Code

```bash
docker build -f claude-code/Dockerfile -t claude-code:local .
```

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

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f hermes/Dockerfile \
  -t <registry>/hermes-lite:latest \
  --push \
  .
```

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

The supported architectures are:

- `linux/amd64`
- `linux/arm64`

This publishes a single multi-arch image manifest, so both architectures are available under the same tag.
