# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS clawmanager-agent-builder

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

FROM lscr.io/linuxserver/webtop:ubuntu-kde

ARG HERMES_VERSION=0.21.0
ARG HERMES_GIT_REF=v2026.8.31
ARG HERMES_GIT_COMMIT=29112bef099274229cadff79cdff7bf7b99c4b77
ARG NODE_MAJOR=22
ARG TARGETARCH
ARG DEBIAN_FRONTEND=noninteractive

LABEL io.clawmanager.hermes.version=${HERMES_VERSION} \
    io.clawmanager.hermes.tag=${HERMES_GIT_REF} \
    io.clawmanager.hermes.commit=${HERMES_GIT_COMMIT} \
    io.clawmanager.hermes.interface="desktop"

ENV HERMES_HOME=/config/.hermes \
    HERMES_SKILL_DIRS=/config/hermes/skills:/config/.hermes/skills \
    CLAWMANAGER_RUNTIME_TYPE=hermes \
    PLAYWRIGHT_BROWSERS_PATH=/usr/local/share/ms-playwright \
    PUID=1000 \
    PGID=1000 \
    TITLE="Hermes Runtime" \
    SUBFOLDER="/" \
    HERMES_VERSION=${HERMES_VERSION} \
    HERMES_GIT_REF=${HERMES_GIT_REF} \
    HERMES_GIT_COMMIT=${HERMES_GIT_COMMIT} \
    CSC_IDENTITY_AUTO_DISCOVERY=false \
    PATH=/opt/node/bin:/usr/local/bin:${PATH}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bash \
        build-essential \
        ca-certificates \
        curl \
        ffmpeg \
        git \
        libffi-dev \
        libjpeg-dev \
        libolm-dev \
        libssl-dev \
        libxml2-dev \
        libxslt1-dev \
        openssh-client \
        pkg-config \
        portaudio19-dev \
        ripgrep \
        sudo \
        tar \
        unzip \
        xz-utils \
        zlib1g-dev; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "${arch}" in \
        amd64|x86_64) node_arch="x64" ;; \
        arm64|aarch64) node_arch="arm64" ;; \
        arm|armhf) node_arch="armv7l" ;; \
        *) echo "Unsupported architecture for Node.js: ${arch}" >&2; exit 1 ;; \
    esac; \
    node_index="https://nodejs.org/dist/latest-v${NODE_MAJOR}.x/"; \
    node_tar="$(curl -fsSL "${node_index}" | grep -oE "node-v${NODE_MAJOR}\.[0-9]+\.[0-9]+-linux-${node_arch}\.tar\.xz" | head -1)"; \
    test -n "${node_tar}"; \
    curl -fsSL "${node_index}${node_tar}" -o /tmp/node.tar.xz; \
    mkdir -p /opt/node; \
    tar -xJf /tmp/node.tar.xz -C /opt/node --strip-components=1; \
    ln -sf /opt/node/bin/node /usr/local/bin/node; \
    ln -sf /opt/node/bin/npm /usr/local/bin/npm; \
    ln -sf /opt/node/bin/npx /usr/local/bin/npx; \
    node --version; \
    npm --version; \
    rm -f /tmp/node.tar.xz

RUN set -eux; \
    mkdir -p "${HERMES_HOME}" "${PLAYWRIGHT_BROWSERS_PATH}"; \
    export HOME=/root; \
    curl -fsSL "https://raw.githubusercontent.com/NousResearch/hermes-agent/${HERMES_GIT_REF}/scripts/install.sh" \
        | bash -s -- --branch "${HERMES_GIT_REF}" --hermes-home "${HERMES_HOME}" --skip-setup; \
    test "$(git -C /usr/local/lib/hermes-agent rev-parse HEAD)" = "${HERMES_GIT_COMMIT}"; \
    /usr/local/lib/hermes-agent/venv/bin/python -m ensurepip --upgrade; \
    /usr/local/lib/hermes-agent/venv/bin/python -m pip install --no-cache-dir "hermes-agent[dingtalk,messaging,matrix,pty,web,wecom]==${HERMES_VERSION}"; \
    hermes --help >/dev/null

COPY hermes/patches/hermes-agent/apply_team_completion_stop.py /tmp/apply_team_completion_stop.py

RUN set -eux; \
    /usr/local/lib/hermes-agent/venv/bin/python /tmp/apply_team_completion_stop.py \
        /usr/local/lib/hermes-agent/agent/conversation_loop.py; \
    cd /usr/local/lib/hermes-agent; \
    hermes desktop --build-only; \
    test -d /usr/local/lib/hermes-agent/apps/desktop/release; \
    find /usr/local/lib/hermes-agent/apps/desktop/release -type f -name Hermes -print -quit | grep -q .; \
    sandbox_path="$(find /usr/local/lib/hermes-agent/apps/desktop/release -type f -name chrome-sandbox -print -quit)"; \
    test -n "${sandbox_path}"; \
    chown root:root "${sandbox_path}"; \
    chmod 4755 "${sandbox_path}"; \
    chmod -R a+rX /usr/local/lib/hermes-agent "${PLAYWRIGHT_BROWSERS_PATH}"; \
    chown -R abc:abc "${HERMES_HOME}" || true; \
    rm -rf /root/.cache /root/.npm /tmp/*

COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
# Package the canonical adapter directly so the image and its contract tests
# cannot drift through a second copied source tree.
COPY plugins/hermes-redis-team/ /tmp/hermes-vendor-plugins/redis_team/
COPY hermes/rootfs/ /

ENV SHELL=/bin/bash

RUN set -eux; \
    hermes_python="$(readlink -f /usr/local/lib/hermes-agent/venv/bin/python)"; \
    hermes_python_root="$(dirname "$(dirname "${hermes_python}")")"; \
    hermes_new_python_root="/opt/uv/python/$(basename "${hermes_python_root}")"; \
    mkdir -p /opt/uv/python /usr/local/lib/hermes-agent/plugins/platforms/redis_team; \
    cp -a "${hermes_python_root}" /opt/uv/python/; \
    ln -sfn "${hermes_new_python_root}/bin/$(basename "${hermes_python}")" /usr/local/lib/hermes-agent/venv/bin/python; \
    sed -i "s|/root/.local/share/uv/python/[^/]*/bin|${hermes_new_python_root}/bin|g" /usr/local/lib/hermes-agent/venv/pyvenv.cfg; \
    chmod -R a+rX /opt/uv /usr/local/lib/hermes-agent/venv; \
    rm -rf /root/.local/share/uv/python; \
    cp -a /tmp/hermes-vendor-plugins/redis_team/. /usr/local/lib/hermes-agent/plugins/platforms/redis_team/; \
    chmod -R a+rX /usr/local/lib/hermes-agent/plugins/platforms/redis_team; \
    rm -rf /tmp/hermes-vendor-plugins; \
    find /etc/s6-overlay/s6-rc.d -type f -exec sed -i 's/\r$//' {} +; \
    sed -i 's/\r$//' \
        /usr/local/bin/wrapped-chromium \
        /usr/local/bin/hermes-apply-runtime-config \
        /usr/local/bin/start-hermes-desktop \
        /usr/local/bin/start-hermes-gateway \
        /custom-cont-init.d/99-hermes-config \
        /etc/s6-overlay/s6-rc.d/hermes-agent/run \
        /etc/s6-overlay/s6-rc.d/hermes-desktop/run \
        /etc/s6-overlay/s6-rc.d/hermes-gateway/run; \
    chmod 755 \
        /usr/local/bin/wrapped-chromium \
        /usr/local/bin/clawmanager-agent \
        /usr/local/bin/hermes-apply-runtime-config \
        /usr/local/bin/start-hermes-desktop \
        /usr/local/bin/start-hermes-gateway \
        /custom-cont-init.d/99-hermes-config \
        /etc/s6-overlay/s6-rc.d/hermes-agent/run \
        /etc/s6-overlay/s6-rc.d/hermes-desktop/run \
        /etc/s6-overlay/s6-rc.d/hermes-gateway/run

EXPOSE 3001
