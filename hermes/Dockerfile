# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS hermes-agent-builder

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN set -eux; \
    arch="${TARGETARCH:-amd64}"; \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${arch}" \
        go build -trimpath -ldflags="-s -w -X main.version=${AGENT_VERSION}" \
        -o /out/hermes-agent ./cmd/hermes-agent

FROM lscr.io/linuxserver/webtop:ubuntu-kde

ARG HERMES_BRANCH=main
ARG NODE_MAJOR=22
ARG TARGETARCH
ARG DEBIAN_FRONTEND=noninteractive

ENV HERMES_HOME=/config/.hermes \
    HERMES_SKILL_DIRS=/config/hermes/skills:/config/.hermes/skills \
    PLAYWRIGHT_BROWSERS_PATH=/usr/local/share/ms-playwright \
    PUID=1000 \
    PGID=1000 \
    TITLE="Hermes Runtime" \
    SUBFOLDER="/" \
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
    curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh \
        | bash -s -- --branch "${HERMES_BRANCH}" --hermes-home "${HERMES_HOME}" --skip-setup; \
    /usr/local/lib/hermes-agent/venv/bin/python -m ensurepip --upgrade; \
    /usr/local/lib/hermes-agent/venv/bin/python -m pip install --no-cache-dir "hermes-agent[dingtalk]"; \
    chmod -R a+rX /usr/local/lib/hermes-agent "${PLAYWRIGHT_BROWSERS_PATH}"; \
    chown -R abc:abc "${HERMES_HOME}" || true; \
    hermes --help >/dev/null; \
    rm -rf /root/.cache /tmp/*

COPY --from=hermes-agent-builder /out/hermes-agent /usr/local/bin/hermes-agent
# Canonical source lives at ../plugins/hermes-redis-team. This vendor copy keeps
# the existing hermes/ Docker build context self-contained.
COPY vendor-plugins/redis_team/ /tmp/hermes-vendor-plugins/redis_team/
COPY rootfs/ /

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
    sed -i 's/\r$//' \
        /usr/local/bin/hermes-apply-runtime-config \
        /usr/local/bin/start-hermes-terminal \
        /usr/local/bin/start-hermes-gateway \
        /custom-cont-init.d/99-hermes-config \
        /etc/s6-overlay/s6-rc.d/hermes-agent/run \
        /etc/s6-overlay/s6-rc.d/hermes-gateway/run; \
    chmod 755 \
        /usr/local/bin/hermes-agent \
        /usr/local/bin/hermes-apply-runtime-config \
        /usr/local/bin/start-hermes-terminal \
        /usr/local/bin/start-hermes-gateway \
        /custom-cont-init.d/99-hermes-config \
        /etc/s6-overlay/s6-rc.d/hermes-agent/run \
        /etc/s6-overlay/s6-rc.d/hermes-gateway/run

EXPOSE 3001
