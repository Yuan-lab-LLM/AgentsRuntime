# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS agent-builder

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH
ARG AGENT_VERSION=dev

COPY openclaw/go.mod openclaw/go.sum ./
RUN go mod download

COPY openclaw/cmd ./cmd
COPY openclaw/internal ./internal

RUN set -eux; \
    arch="${TARGETARCH:-amd64}"; \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${arch}" \
      go build -trimpath -ldflags="-s -w -X github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol.AgentVersion=openclaw-agent-${AGENT_VERSION}" \
      -o /out/openclaw-agent ./cmd/openclaw-agent

FROM node:22-alpine

ARG OPENCLAW_VERSION=v2026.5.4

ENV HOME=/config \
    SHELL=/bin/bash \
    OPENCLAW_AGENT_DROP_USER_NAME=abc \
    CLAWMANAGER_AGENT_PERSISTENT_DIR=/config \
    OPENCLAW_AGENT_CONFIG_PATH=/etc/openclaw-agent/config.yaml \
    OPENCLAW_AGENT_DATA_DIR=/config/openclaw-shell-agent \
    OPENCLAW_AGENT_RUNTIME_TYPE=openclaw-shell \
    OPENCLAW_AGENT_RUNTIME_NAME="OpenClaw Shell" \
    OPENCLAW_AGENT_DESKTOP_BASE=none \
    OPENCLAW_AGENT_RUNTIME_PORT=18789 \
    OPENCLAW_AGENT_BROWSER_AUTO_LAUNCH_ENABLED=false \
    OPENCLAW_AGENT_LOCAL_HTTP_BIND=0.0.0.0:18080 \
    OPENCLAW_AGENT_OPENCLAW_HEALTH_URL=http://127.0.0.1:18789/health \
    OPENCLAW_AGENT_OPENCLAW_DOCTOR_POLICY=auto \
    CLAWMANAGER_TMUX_SESSION=openclaw-shell \
    CLAWMANAGER_TMUX_HISTORY_LIMIT=100000 \
    OPENCLAW_SHELL_WORKDIR=/team \
    TERM=xterm-256color \
    COLORTERM=truecolor \
    FORCE_COLOR=1 \
    CLICOLOR=1 \
    CLICOLOR_FORCE=1 \
    NPM_CONFIG_COLOR=always \
    YARN_FORCE_COLOR=1 \
    XDG_CACHE_HOME=/config/.cache \
    XDG_CONFIG_HOME=/config/.config \
    XDG_DATA_HOME=/config/.local/share \
    NPM_CONFIG_CACHE=/config/.cache/npm

RUN apk add --no-cache \
      bash \
      ca-certificates \
      chromium \
      curl \
      git \
      ncurses-terminfo \
      openssh-client \
      ripgrep \
      tini \
      tmux \
      tzdata \
      unzip \
    && apk add --no-cache --virtual .openclaw-build-deps \
      g++ \
      make \
      python3 \
    && npm install -g "openclaw@${OPENCLAW_VERSION}" \
    && npm cache clean --force \
    && apk del .openclaw-build-deps

COPY openclaw/defaults-template/.openclaw /defaults/.openclaw
COPY openclaw-shell/config/openclaw-shell-agent /defaults/openclaw-shell-agent
COPY openclaw-shell/vendor-plugins/openclaw-redis-team.tgz /tmp/openclaw-redis-team.tgz
COPY openclaw-shell/scripts/enable-redis-team-config.mjs /tmp/enable-redis-team-config.mjs
COPY openclaw-shell/scripts/openclaw-shell-entrypoint /usr/local/bin/openclaw-shell-entrypoint
COPY openclaw-shell/scripts/openclaw-shell-login /usr/local/lib/openclaw-shell/login.sh
COPY --from=agent-builder /out/openclaw-agent /usr/local/bin/openclaw-agent

RUN set -eux; \
    addgroup -g 1000 abc 2>/dev/null || true; \
    adduser -D -u 1000 -G abc -h /config -s /bin/bash abc 2>/dev/null || true; \
    if ! getent group abc >/dev/null 2>&1 && getent group node >/dev/null 2>&1; then sed -i 's/^node:/abc:/' /etc/group; fi; \
    if ! id abc >/dev/null 2>&1 && id node >/dev/null 2>&1; then sed -i 's/^node:/abc:/' /etc/passwd; fi; \
    if ! getent group abc >/dev/null 2>&1; then echo 'abc:x:1000:' >>/etc/group; fi; \
    if ! id abc >/dev/null 2>&1; then echo 'abc:x:1000:1000:abc:/config:/bin/bash' >>/etc/passwd; fi; \
    sed -i 's#^\(abc:[^:]*:[^:]*:[^:]*:[^:]*:\)[^:]*:[^:]*#\1/config:/bin/bash#' /etc/passwd; \
    sed -i 's/\r$//' /usr/local/bin/openclaw-shell-entrypoint; \
    sed -i 's/\r$//' /usr/local/lib/openclaw-shell/login.sh; \
    chmod 755 /usr/local/bin/openclaw-shell-entrypoint /usr/local/bin/openclaw-agent; \
    chmod 644 /usr/local/lib/openclaw-shell/login.sh; \
    mkdir -p \
      /config/.cache/npm \
      /config/.config \
      /config/.local/share \
      /config/.openclaw/extensions \
      /config/.openclaw/workspace/skills \
      /config/openclaw-shell-agent/history \
      /config/openclaw-shell-agent/logs \
      /etc/profile.d \
      /etc/openclaw-agent; \
    printf '%s\n' \
      'case "${TERM:-}" in ""|dumb|unknown) export TERM=xterm-256color ;; esac' \
      'export COLORTERM="${COLORTERM:-truecolor}"' \
      'export FORCE_COLOR="${FORCE_COLOR:-1}"' \
      'export CLICOLOR="${CLICOLOR:-1}"' \
      'export CLICOLOR_FORCE="${CLICOLOR_FORCE:-1}"' \
      'export NPM_CONFIG_COLOR="${NPM_CONFIG_COLOR:-always}"' \
      'export YARN_FORCE_COLOR="${YARN_FORCE_COLOR:-1}"' \
      'umask 0002' \
      'unset NO_COLOR' \
      >/etc/profile.d/openclaw-shell-color.sh; \
    printf '%s\n' 'set enable-bracketed-paste off' >/etc/inputrc; \
    printf '%s\n' \
      'set -g default-terminal "tmux-256color"' \
      'set-option -g history-limit 100000' \
      'set -g mouse on' \
      'set -as terminal-overrides ",xterm-256color:Tc"' \
      >/etc/tmux.conf; \
    printf '%s\n' \
      '[ -r /usr/local/lib/openclaw-shell/login.sh ] && . /usr/local/lib/openclaw-shell/login.sh' \
      >/etc/profile.d/openclaw-shell-login.sh; \
    chmod 644 /etc/profile.d/openclaw-shell-color.sh; \
    chmod 644 /etc/profile.d/openclaw-shell-login.sh; \
    chmod 644 /etc/inputrc; \
    chmod 644 /etc/tmux.conf; \
    cp /defaults/openclaw-shell-agent/config.yaml /etc/openclaw-agent/config.yaml; \
    HOME=/defaults openclaw plugins install /tmp/openclaw-redis-team.tgz; \
    HOME=/defaults openclaw plugins install @dingtalk-real-ai/dingtalk-connector \
      || echo "warning: dingtalk-connector preinstall failed; continuing without bundled connector"; \
    HOME=/defaults openclaw plugins install @wecom/wecom-openclaw-plugin@beta \
      || echo "warning: wecom-openclaw-plugin preinstall failed; continuing without bundled connector"; \
    HOME=/defaults openclaw plugins install @openclaw/feishu@2026.5.4 \
      || echo "warning: feishu preinstall failed; continuing without bundled connector"; \
    node /tmp/enable-redis-team-config.mjs /defaults/.openclaw/openclaw.json; \
    rm -f /tmp/openclaw-redis-team.tgz /tmp/enable-redis-team-config.mjs; \
    chown -R abc:abc /config /etc/openclaw-agent /defaults/.openclaw

EXPOSE 18789 18080

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/openclaw-shell-entrypoint"]
