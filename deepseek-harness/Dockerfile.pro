# syntax=docker/dockerfile:1.7

ARG WEBTOP_RUNTIME_BASE_IMAGE=ghcr.io/yuan-lab-llm/agentsruntime/openclaw:latest

FROM golang:1.26-bookworm AS agent-builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src/openclaw
COPY openclaw/go.mod openclaw/go.sum ./
RUN go mod download
COPY openclaw/cmd ./cmd
COPY openclaw/internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/openclaw-agent ./cmd/openclaw-agent

FROM ${WEBTOP_RUNTIME_BASE_IMAGE}

ARG DSH_VERSION=0.1.0-rc.6
RUN set -eux; \
    npm install --global "@deepseek-ai/dsh@${DSH_VERSION}"; \
    dsh --version; \
    npm cache clean --force

COPY deepseek-harness/config/managed.cordis.patch.yml /usr/local/share/deepseek-harness/managed.cordis.patch.yml
COPY deepseek-harness/scripts/start-deepseek-harness-pro /usr/local/bin/start-deepseek-harness-pro
COPY deepseek-harness/config/pro-agent-config.yaml /defaults/openclaw-agent/config.yaml
COPY deepseek-harness/config/deepseek-harness-browser.desktop /defaults/.config/autostart/deepseek-harness-browser.desktop
COPY --from=agent-builder /out/openclaw-agent /usr/local/bin/openclaw-agent

RUN set -eux; \
    sed -i 's/\r$//' /usr/local/bin/start-deepseek-harness-pro; \
    chmod 0755 /usr/local/bin/start-deepseek-harness-pro /usr/local/bin/openclaw-agent; \
    rm -f /defaults/.config/autostart/openclaw-browser.desktop; \
    rm -f /etc/openclaw-agent/config.yaml

ENV CLAWMANAGER_RUNTIME_TYPE=deepseek-harness \
    DSH_HOME=/config/.dsh \
    DSH_TELEMETRY_DISABLED=1 \
    TITLE="DeepSeek Harness Pro"
