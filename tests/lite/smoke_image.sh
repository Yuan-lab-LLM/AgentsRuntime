#!/usr/bin/env bash
# Feed to bash inside a built image; no Docker socket or host mounts inside it.
set -euo pipefail
runtime="${1:?usage: smoke_image.sh runtime}"
[[ "${CLAWMANAGER_RUNTIME_TYPE:-}" == "${runtime}" ]]
test -x /usr/local/bin/clawmanager-agent
for tool in bash bwrap unshare mount setpriv tini git rg curl ssh cc make python3; do
  command -v "${tool}" >/dev/null
done
test "$(bash -lc 'printf terminal-ok')" = terminal-ok
for desktop in Xorg startplasma-wayland kwin_wayland s6-svscan; do
  if command -v "${desktop}" >/dev/null; then
    echo "unexpected desktop service: ${desktop}" >&2
    exit 1
  fi
done

case "${runtime}" in
  openclaw)
    test "$(node --version)" = v24.21.0
    node -e 'if (require("/usr/local/lib/node_modules/openclaw/package.json").version !== "2026.10.2") process.exit(1)'
    for patch in patch_workspace_publication_102 patch_upgrade_plugin_scratch_102 patch_memory_core_startup_migration patch_browser_proxy_navigation; do
      node "/usr/local/share/openclaw/patches/${patch}.mjs" --verify
    done
    node /usr/local/share/openclaw/patches/test_managed_plugin_payload_102.mjs
    node /usr/local/share/openclaw/patches/test_redis_team_plugin_hooks.mjs
    node /usr/local/share/openclaw/patches/test_redis_team_channel_config.mjs /defaults/.openclaw/openclaw.json
    # Match the existing Lite browser's headless/noSandbox configuration.
    timeout 30 chromium --headless --no-sandbox --disable-gpu --disable-dev-shm-usage --dump-dom about:blank | rg '<html'
    ;;
  hermes)
    python /usr/local/share/hermes-lite/verify_lite_release.py \
      --lock /usr/local/share/hermes-lite/source-lock.json --runtime --verify-release
    python /usr/local/share/hermes-lite/tests/check_lite_tool_executor.py
    python /usr/local/share/hermes-lite/tests/smoke_lite_non_native.py
    python /usr/local/share/hermes-lite/tests/smoke_lite_image.py
    ;;
  opencode)
    opencode --version | rg '(^|[^0-9])1\.18\.13([^0-9]|$)'
    test -x /usr/local/bin/start-opencode-web
    temporary="$(mktemp -d)"
    trap '[[ -z "${server_pid:-}" ]] || kill "${server_pid}" 2>/dev/null || true; rm -rf "${temporary}"' EXIT
    # Start the real native Web/PTY server with disposable, authenticated state.
    HOME="${temporary}/home" CLAWMANAGER_WORKSPACE_PATH="${temporary}/workspace" \
      OPENCODE_SERVER_PASSWORD=smoke-local-only HOST=127.0.0.1 CLAWMANAGER_GATEWAY_PORT=20000 \
      start-opencode-web >"${temporary}/server.log" 2>&1 &
    server_pid=$!
    ready=false
    for _ in $(seq 1 90); do
      kill -0 "${server_pid}" || { cat "${temporary}/server.log"; exit 1; }
      if curl -fsS --user opencode:smoke-local-only http://127.0.0.1:20000/global/health >/dev/null; then
        ready=true
        break
      fi
      sleep 1
    done
    [[ "${ready}" == true ]]
    test "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:20000/global/health)" = 401
    ;;
  deepseek-harness)
    node -e 'if (require("/usr/local/lib/node_modules/@deepseek-ai/dsh/package.json").version !== "0.1.2-alpha.3") process.exit(1)'
    test "$(pnpm --version)" = 11.25.0
    test -x /usr/local/libexec/clawmanager-agent-image
    test -x /usr/local/bin/start-deepseek-harness-gateway
    test -r /usr/local/share/deepseek-harness/managed.cordis.patch.yml
    prepare-deepseek-harness-resources
    # Verify that the runtime-specific proxy command is assembled into this binary.
    proxy_help="$(/usr/local/libexec/clawmanager-agent-image dsh-web-proxy --help 2>&1 || true)"
    [[ "${proxy_help}" == *"Usage of dsh-web-proxy"* ]]
    ;;
  *) echo "unknown runtime: ${runtime}" >&2; exit 64 ;;
esac
echo "PASS: ${runtime} Lite image payload and runtime smoke"
