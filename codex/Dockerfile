ARG GO_BUILDER_IMAGE=golang:1.26-bookworm
FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS clawmanager-agent-builder
WORKDIR /src/clawmanager-agent
ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev
COPY clawmanager-agent ./
RUN go mod download && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w -X main.version=${AGENT_VERSION}" -o /out/clawmanager-agent ./cmd/clawmanager-agent

FROM lscr.io/linuxserver/webtop:ubuntu-kde
ARG CODEX_VERSION=latest
ENV CODEX_HOME=/config/.codex \
    CLAWMANAGER_AGENT_RUNTIME_TYPE=codex \
    CLAWMANAGER_PROJECT_PATH=/config/workspace \
    PUID=1000 PGID=1000 TITLE="Codex" SUBFOLDER="/" PATH=/usr/local/bin:${PATH}
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git nodejs openssh-client ripgrep && rm -rf /var/lib/apt/lists/* \
    && npm install -g "@openai/codex@${CODEX_VERSION}" && codex --version
COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
COPY codex/rootfs/ /
RUN chmod 755 /usr/local/bin/clawmanager-agent /usr/local/bin/start-codex-terminal /custom-cont-init.d/99-codex-config /etc/s6-overlay/s6-rc.d/codex-agent/run /etc/s6-overlay/s6-rc.d/codex-terminal/run \
    && mkdir -p /var/lib/clawmanager-agent /config/.codex /config/workspace \
    && chown -R abc:abc /config/.codex /config/workspace || true
EXPOSE 3001
