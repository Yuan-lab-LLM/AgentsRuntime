ARG NODE_BASE_IMAGE=node:22.23.2-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5
ARG GO_BUILDER_IMAGE=golang:1.26.8-bookworm
ARG WEBTOP_BASE_IMAGE=lscr.io/linuxserver/webtop:ubuntu-kde
ARG BUN_BUILD_IMAGE=oven/bun:1.4.2
FROM ${NODE_BASE_IMAGE} AS team-node

FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS clawmanager-agent-builder

WORKDIR /src/legacy-agent

ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev

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

FROM ${BUN_BUILD_IMAGE} AS native-team-builder
USER root
ARG TARGETARCH
RUN command -v git && command -v python3 && command -v gcc && command -v g++ && command -v make \
 || (apt-get update && apt-get install -y --no-install-recommends git python3 build-essential ca-certificates)
COPY opencode/model-governance/ /governance/
RUN bash /governance/build-native.sh

FROM ${WEBTOP_BASE_IMAGE}

ARG OPENCODE_VERSION=2.0.27
ARG TARGETARCH
ARG DEBIAN_FRONTEND=noninteractive

ENV OPENCODE_RUNTIME_MAJOR=2 \
    OPENCODE_CONFIG_DIR=/config/.opencode \
    CLAWMANAGER_RUNTIME_TYPE=opencode \
    PUID=1000 \
    PGID=1000 \
    TITLE="OpenCode Runtime" \
    SUBFOLDER="/" \
    PATH=/usr/local/bin:${PATH}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        openssh-client \
        python3 \
        ripgrep \
        tar \
        unzip \
        xz-utils; \
    rm -rf /var/lib/apt/lists/*


COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
COPY --chmod=755 --from=native-team-builder /out/opencode /usr/local/bin/opencode
COPY --from=native-team-builder /out/governance-manifest.json /usr/local/share/clawmanager/opencode-governance-manifest.json
RUN opencode --version | grep -Fx "opencode v${OPENCODE_VERSION}"
ENV CLAWMANAGER_MODEL_GOVERNANCE_PROTOCOL=1 CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=opencode-team-v1
COPY opencode/rootfs/ /

ENV SHELL=/bin/bash

RUN set -eux; \
    find /etc/s6-overlay/s6-rc.d -type f -exec sed -i 's/\r$//' {} +; \
    sed -i 's/\r$//' \
        /usr/local/bin/start-opencode-web \
        /usr/local/bin/start-opencode-pro-web \
        /usr/local/bin/start-opencode-terminal \
        /usr/local/bin/opencode-pro-session.py \
        /usr/local/bin/opencode-apply-runtime-config \
        /custom-cont-init.d/99-opencode-config \
        /etc/s6-overlay/s6-rc.d/opencode-agent/run \
        /etc/s6-overlay/s6-rc.d/opencode-web/run \
        /etc/s6-overlay/s6-rc.d/opencode-terminal/run; \
    chmod 755 \
        /usr/local/bin/clawmanager-agent \
        /usr/local/bin/opencode \
        /usr/local/bin/start-opencode-web \
        /usr/local/bin/start-opencode-pro-web \
        /usr/local/bin/start-opencode-terminal \
        /usr/local/bin/opencode-pro-session.py \
        /usr/local/bin/opencode-apply-runtime-config \
        /custom-cont-init.d/99-opencode-config \
        /etc/s6-overlay/s6-rc.d/opencode-agent/run \
        /etc/s6-overlay/s6-rc.d/opencode-web/run \
        /etc/s6-overlay/s6-rc.d/opencode-terminal/run; \
    mkdir -p /var/lib/clawmanager-agent /config/.opencode; \
    chown -R abc:abc /config/.opencode || true

EXPOSE 3001

COPY --from=team-node /usr/local/bin/node /usr/local/bin/node
COPY opencode/team/ /usr/local/share/clawmanager/opencode-team/
COPY plugins/openclaw-redis-team/dist/index.js /tmp/clawmanager-team-protocol-source.js
RUN node /usr/local/share/clawmanager/opencode-team/build-protocol.mjs /usr/local/share/clawmanager/opencode-team/protocol.mjs /tmp/clawmanager-team-protocol-source.js \
 && rm /tmp/clawmanager-team-protocol-source.js
