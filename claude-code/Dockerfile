ARG GO_BUILDER_IMAGE=golang:1.26-bookworm
FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS clawmanager-agent-builder
WORKDIR /src/clawmanager-agent
ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev
COPY clawmanager-agent ./
RUN go mod download && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w -X main.version=${AGENT_VERSION}" -o /out/clawmanager-agent ./cmd/clawmanager-agent

FROM lscr.io/linuxserver/webtop:ubuntu-kde
ARG CLAUDE_CODE_VERSION=latest
ENV CLAUDE_CONFIG_DIR=/config/.claude \
    CLAWMANAGER_AGENT_RUNTIME_TYPE=claude-code \
    CLAWMANAGER_PROJECT_PATH=/config/workspace \
    PUID=1000 PGID=1000 TITLE="Claude Code" SUBFOLDER="/" PATH=/usr/local/bin:${PATH}
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git nodejs openssh-client ripgrep && rm -rf /var/lib/apt/lists/* \
    && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" && claude --version
COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
COPY claude-code/rootfs/ /
RUN chmod 755 /usr/local/bin/clawmanager-agent /usr/local/bin/start-claude-code-terminal /custom-cont-init.d/99-claude-code-config /etc/s6-overlay/s6-rc.d/claude-code-agent/run /etc/s6-overlay/s6-rc.d/claude-code-terminal/run \
    && mkdir -p /var/lib/clawmanager-agent /config/.claude /config/workspace \
    && chown -R abc:abc /config/.claude /config/workspace || true
EXPOSE 3001
