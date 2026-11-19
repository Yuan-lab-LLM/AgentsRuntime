# syntax=docker/dockerfile:1.7

ARG WEBTOP_RUNTIME_BASE_IMAGE=ghcr.io/yuan-lab-llm/agentsruntime/openclaw:latest

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS clawmanager-agent-builder

ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev

WORKDIR /src/clawmanager-agent
COPY clawmanager-agent ./
RUN go mod download
RUN set -eux; \
    arch="${TARGETARCH:-amd64}"; \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${arch}" \
      go build -trimpath -ldflags="-s -w -X main.version=${AGENT_VERSION}" \
      -o /out/clawmanager-agent ./cmd/clawmanager-agent

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

ARG DSH_VERSION=0.2.1-rc.3
ARG PNPM_VERSION=11.25.0
LABEL io.clawmanager.deepseek-harness.version="${DSH_VERSION}"
RUN set -eux; \
    node -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (!(major >= 24 || (major === 22 && minor >= 19))) process.exit(1)'; \
    npm install --global "@deepseek-ai/dsh@${DSH_VERSION}" "pnpm@${PNPM_VERSION}"; \
    dsh --version; \
    npm cache clean --force

COPY deepseek-harness/config/managed.cordis.patch.yml /usr/local/share/deepseek-harness/managed.cordis.patch.yml
COPY deepseek-harness/scripts/patch-model-reference.mjs deepseek-harness/scripts/managed-model-reference.mjs /usr/local/share/deepseek-harness/
RUN node /usr/local/share/deepseek-harness/patch-model-reference.mjs /usr/local/lib/node_modules/@deepseek-ai/dsh \
    && node /usr/local/share/deepseek-harness/patch-model-reference.mjs /usr/local/lib/node_modules/@deepseek-ai/dsh --verify
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
    TITLE="DeepSeek Harness Pro"
