# OpenClaw Shell Runtime

This directory defines the Alpine-based OpenClaw shell image. It deliberately omits Webtop, KasmVNC, and browser autostart. The Docker build context is the repository root so the image can reuse the `openclaw-agent` implementation from `openclaw/`.

## Build

```bash
docker build -f openclaw-shell/Dockerfile -t openclaw-shell:local .
```

## Runtime contract

- Persistent directory: `/config`
- Web terminal user: `root`. The agent/gateway still drops to `abc` (`uid=1000,gid=1000`) so Team task artifacts are not written as root by default.
- Agent state: `/config/openclaw-shell-agent`
- Runtime type reported to ClawManager: `openclaw-shell`
- OpenClaw gateway port: `18789`
- Agent local health/debug port: `18080`

Set `CLAWMANAGER_AGENT_ENABLED=true` plus the standard ClawManager agent and AI Gateway environment variables to enable managed runtime integration.

Interactive shells attach to a persistent `tmux` session named `openclaw-shell`, or `CLAWMANAGER_TMUX_SESSION` when set. The global tmux `history-limit` is set before session creation and defaults to `100000`, with `CLAWMANAGER_TMUX_HISTORY_LIMIT` available as an override. Color support is enabled with `TERM=xterm-256color`, command history is persisted under `/config/openclaw-shell-agent/history`, and pane output is mirrored to `/config/openclaw-shell-agent/logs/openclaw-shell.typescript`. ClawManager Web Shell must render the PTY stream with full terminal emulation; see `WEB_TMUX_TERMINAL_REQUIREMENTS.md`.

The default shell workdir is `/team` when that shared mount exists, otherwise it falls back to `/config`. `umask 0002` is applied, and the entrypoint repairs `/team` ownership/group-write permissions plus directory setgid bits so shared files remain editable across desktop and shell runtimes.
