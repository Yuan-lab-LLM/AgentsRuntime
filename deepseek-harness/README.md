# DeepSeek Harness Runtime

This directory owns the ClawManager runtime images for DeepSeek Harness.

- `Dockerfile.lite` builds the pooled, headless Lite runtime. The shared
  `clawmanager-agent` starts one isolated `dsh web` process per instance. DSH
  listens only on a private loopback port; the agent-facing gateway validates
  the ClawManager instance token and holds DSH's browser-session cookie inside
  the runtime process boundary.
- `Dockerfile.pro` builds the dedicated Webtop Pro runtime. A desktop-only proxy
  listens on `127.0.0.1:3080`; DSH uses a private loopback port. The desktop opens
  the stable proxy URL after authenticated readiness, without a launch token.

Both images pin `@deepseek-ai/dsh@0.2.1-rc.3` and apply the managed Cordis provider patch
from `config/managed.cordis.patch.yml`. The Lite image also pins the `pnpm` CLI
required by dsh-market so pooled non-root instances never need to modify the
system Node installation at runtime. ClawManager supplies the OpenAI-compatible
base URL, instance credential, allowed model list, context windows, and image
input capabilities at runtime. Lite uses Node 24.21.0; Pro checks that its base
provides Node 22.19+ (22.x) or Node 24+. Both install a pinned pnpm CLI.

## Managed configuration and access

`prepare-managed-config.mjs` reads the actual composed Web profile with
`--dump-config`, including user configuration. It preserves preset IDs, fields,
and deferred `!!js` expressions, and disables `dsh-tool-web` in every declarative
preset. The generated overlay lives under `$DSH_HOME/.clawmanager/`; it is rebuilt
on every start and never replaces the user's profile files. Restart the instance
after editing preset definitions so the managed overlay is regenerated.

The image overlay controls model routes and defaults. Context windows prefer a
qualified `provider/model` key, then a bare model ID, then the platform default
(128000 when no default is provided). Invalid explicit capacities fail startup.
Vision metadata controls `input: [text, image]`; other models are text-only.

The Lite proxy requires the instance token. The Pro proxy only accepts loopback
clients with a loopback Host and a same-origin browser request; it cannot bind a
wildcard address in desktop mode. Both retain DSH cookies internally and renew
them before expiry, without retrying model/task POSTs. DSH launch tokens are
removed from startup output and network error messages.

The shared desktop supervisor skips OpenClaw doctor/model warmup for this runtime
and probes the configured DSH proxy root instead of `/readyz`. A startup timeout
terminates the failed process instead of reporting it as running.

## Upgrade and rollback

1. Stop a test instance and take an offline copy/snapshot of its entire persistent
   workspace, including hidden files. For Lite this includes `home/.dsh`; for Pro
   it includes `/config/.dsh` and `/config/workspace`. Preserve ownership and modes.
2. Keep the old image digest with that snapshot. Test both a fresh workspace and
   a copied old workspace using the candidate image. Do not initialize with
   `--from-default-profile`: it replaces user profile configuration.
3. Check model selection, continued historical conversations, attachments,
   installed plugins, skills, file operations, and restart/reconnect. Old plugin
   packages may require a version-compatible update; do not automatically bypass
   the upstream compatibility checks.
4. Promote the tested immutable image tag/digest, first to a small pool or one Pro
   instance. Keep the previous image and snapshot through the observation period.
5. To roll back, stop the new runtime and restore BOTH the old image and its
   pre-upgrade workspace snapshot. Downgrading an image against data already
   migrated by a newer Harness is not assumed safe.

## Validation

```bash
node --test deepseek-harness/scripts/managed-config.test.mjs
(cd clawmanager-agent && go test ./internal/dshproxy -count=1)
(cd openclaw && go test ./internal/process ./internal/httpserver ./internal/config -count=1)
```

`tests/smoke.py` runs inside a candidate runtime with both startup scripts installed.
It creates temporary homes and a local model endpoint, checks Lite and desktop
proxy startup/restart, auth boundaries, assets, plugin batches, WebSocket upgrades,
streaming model requests, skill discovery, file reads through a real tool call,
and child cleanup after SIGTERM. It uses no real provider credentials.
`tests/migration.py NODE OLD_DSH_JS NEW_DSH_JS` creates an old-version session,
resumes it with the new CLI, and checks that user configuration, skills and
workspace files are preserved. Full desktop
UI, ARM64 execution, and production-data migration require their own acceptance
environment in addition to these checks.

Build both images from the repository root:

```bash
docker build -f deepseek-harness/Dockerfile.lite -t deepseek-harness-lite:local .
docker build -f deepseek-harness/Dockerfile.pro -t deepseek-harness:local .
```

## CI images

Pushes to `main` that change DeepSeek Harness or either shared agent dependency
automatically publish multi-architecture `deepseek-harness` and
`deepseek-harness-lite` images through `.github/workflows/docker-ghcr.yml`.
Each run updates `latest` and the current Shanghai-date tag.

## Host resource prerequisites

Both images raise the DSH process soft `nofile` limit to `65536` (bounded by
the container hard limit) before starting `dsh web`. Override it with
`DSH_NOFILE_SOFT` only when the deployment has a deliberate lower limit.

Linux inotify limits are node-level kernel settings and cannot be changed by
an unprivileged runtime container. Apply the following persistent settings on
every Kubernetes node that may schedule DeepSeek Harness or desktop runtime
pods:

```text
fs.inotify.max_user_instances = 1024
fs.inotify.max_user_watches = 524288
```

Store them in `/etc/sysctl.d/99-clawmanager-inotify.conf` and apply them with
the operating system's normal sysctl provisioning. The startup guard prints
the effective nofile and inotify values and warns when the host limits are
below these recommendations.
