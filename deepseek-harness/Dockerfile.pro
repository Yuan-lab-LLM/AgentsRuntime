# syntax=docker/dockerfile:1.7

ARG WEBTOP_RUNTIME_BASE_IMAGE=ghcr.io/yuan-lab-llm/agentsruntime/openclaw:latest
ARG GO_BUILDER_IMAGE=golang:1.26.8-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81

FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS native-builder
COPY deepseek-harness/native/package.json deepseek-harness/native/package-lock.json /opt/clawmanager-dsh/
RUN --mount=type=cache,target=/root/.npm \
    npm ci --prefix /opt/clawmanager-dsh --ignore-scripts --no-audit --no-fund
COPY deepseek-harness/native/source-lock.json deepseek-harness/scripts/verify-native.mjs /opt/clawmanager-dsh/
RUN node /opt/clawmanager-dsh/verify-native.mjs /opt/clawmanager-dsh/native-manifest.json

FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS clawmanager-agent-builder
ARG GOPROXY

ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev

WORKDIR /src/legacy-agent
COPY clawmanager-agent /src/clawmanager-agent
COPY legacy-agent /src/legacy-agent
COPY openclaw/go.mod openclaw/go.sum /src/openclaw/
COPY openclaw/lite /src/openclaw/lite
COPY hermes/go.mod hermes/go.sum /src/hermes/
COPY hermes/lite /src/hermes/lite
COPY opencode/go.mod /src/opencode/
COPY opencode/lite /src/opencode/lite
COPY deepseek-harness/go.mod /src/deepseek-harness/
COPY deepseek-harness/lite /src/deepseek-harness/lite
COPY deepseek-harness/dshproxy /src/deepseek-harness/dshproxy
RUN go mod download
RUN set -eux; \
    arch="${TARGETARCH:-amd64}"; \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${arch}" \
      go build -trimpath -ldflags="-s -w -X main.version=${AGENT_VERSION}" \
      -o /out/clawmanager-agent ./cmd

FROM ${GO_BUILDER_IMAGE} AS agent-builder
ARG GOPROXY

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src/openclaw
# Resolve openclaw/go.mod's local replacement in this independent stage.
COPY clawmanager-agent/go.mod /src/clawmanager-agent/go.mod
COPY openclaw/go.mod openclaw/go.sum ./
RUN go mod download
COPY openclaw/cmd ./cmd
COPY openclaw/internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/openclaw-agent ./cmd/openclaw-agent

FROM ${WEBTOP_RUNTIME_BASE_IMAGE}

ARG VCS_REF=unknown
LABEL io.clawmanager.deepseek-harness.version="0.2.1-rc.3" org.opencontainers.image.revision=$VCS_REF
COPY --from=native-builder /usr/local/bin/node /usr/local/bin/node
COPY --from=native-builder /opt/clawmanager-dsh/ /opt/clawmanager-dsh/
RUN set -eux; \
    mkdir -p /usr/local/lib/node_modules/@deepseek-ai; \
    test ! -e /usr/local/lib/node_modules/@deepseek-ai/dsh; \
    ln -s /opt/clawmanager-dsh/node_modules/@deepseek-ai/dsh /usr/local/lib/node_modules/@deepseek-ai/dsh; \
    ln -s /opt/clawmanager-dsh/node_modules/.bin/dsh /usr/local/bin/dsh; \
    ln -sf /opt/clawmanager-dsh/node_modules/.bin/pnpm /usr/local/bin/pnpm; \
    dsh --version; \
    npm cache clean --force

COPY deepseek-harness/config/managed.cordis.patch.yml /usr/local/share/deepseek-harness/managed.cordis.patch.yml
COPY deepseek-harness/scripts/prepare-managed-config.mjs /usr/local/share/deepseek-harness/prepare-managed-config.mjs
COPY deepseek-harness/scripts/prepare-deepseek-harness-resources /usr/local/bin/prepare-deepseek-harness-resources
COPY deepseek-harness/scripts/start-deepseek-harness-pro /usr/local/bin/start-deepseek-harness-pro
COPY deepseek-harness/config/pro-agent-config.yaml /defaults/openclaw-agent/config.yaml
COPY deepseek-harness/config/deepseek-harness-browser.desktop /defaults/.config/autostart/deepseek-harness-browser.desktop
COPY --from=agent-builder /out/openclaw-agent /usr/local/bin/openclaw-agent
COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/libexec/clawmanager-agent-image

RUN set -eux; \
    sed -i 's/\r$//' /usr/local/bin/prepare-deepseek-harness-resources; \
    sed -i 's/\r$//' /usr/local/bin/start-deepseek-harness-pro; \
    chmod 0755 /usr/local/bin/prepare-deepseek-harness-resources /usr/local/bin/start-deepseek-harness-pro /usr/local/bin/openclaw-agent /usr/local/bin/clawmanager-agent; \
    rm -f /defaults/.config/autostart/openclaw-browser.desktop; \
    rm -f /etc/openclaw-agent/config.yaml

ENV CLAWMANAGER_RUNTIME_TYPE=deepseek-harness \
    DSH_HOME=/config/.dsh \
    DSH_NOFILE_SOFT=65536 \
    DSH_TELEMETRY_DISABLED=1 \
    TITLE="DeepSeek Harness Pro" \
    CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=""
