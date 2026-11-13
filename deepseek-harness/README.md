# DeepSeek Harness Runtime

This directory owns the ClawManager runtime images for DeepSeek Harness.

- `Dockerfile.lite` builds the pooled, headless Lite runtime. The shared
  `clawmanager-agent` starts one isolated `dsh web` process per instance. DSH
  listens only on a private loopback port; the agent-facing gateway validates
  the ClawManager instance token and holds DSH's browser-session cookie inside
  the runtime process boundary.
- `Dockerfile.pro` builds the dedicated Webtop Pro runtime. DeepSeek Harness
  runs on loopback port `3080` and opens automatically in the desktop browser.

Both images pin `@deepseek-ai/dsh` **0.2.1-rc.3** and apply the managed Cordis provider patch
from `config/managed.cordis.patch.yml`. The Lite image also pins the `pnpm` CLI
required by dsh-market so pooled non-root instances never need to modify the
system Node installation at runtime. ClawManager supplies the OpenAI-compatible
base URL, instance credential, and allowed model list at runtime.

The complete native dependency graph and package integrities are frozen in
`native/package-lock.json`, including the compatible Cordis4.0.5/HMR0.2.1/Loader1.0.6
family. Builds use `npm ci --ignore-scripts` on Node24.21.0, with the Node image
digest and original sandbox hash in `native/source-lock.json`. The old
`cordis.lock.json`/`vendor/cordis` archives remain historical artifacts and are
not applied over the new native graph. No `--expose-internals` workaround is
needed by this release. The Lite image applies one SHA-pinned sandbox-local
patch: workspace-write commands bind the
outer instance-private `/tmp` instead of creating another temporary filesystem.
The read-only profile and Landlock grants retain their existing constraints.
The Lite image also SHA-pins its settings client patch: managed remote browsers
use the instance-authenticated host settings mirror. Reads remain secret-redacted,
and writes retain the host provider policy and revision checks. Generic loopback
detection and host ownership remain unchanged. The image build runs
`tests/lite/test_deepseek_settings.mjs` against the installed client bundle.

The managed proxy renews its private browser session before request dispatch;
concurrent renewal is shared and a mutating request is never replayed after 401.
The Pro proxy is loopback-only and additionally rejects cross-origin/remote
requests. Both modes preserve significant plugin batch query strings.
`prepare-managed-config.mjs` applies managed rules to the actually composed
presets without replacing user profiles. Model routes retain context-window
and vision capability metadata; unknown CM window0 uses a valid fallback.

This M0/M1 release does **not** enable DSH Team membership or advertise a Team
plugin capability. It preserves the existing Team dispatch/Worker contracts.
See `docs/dsh-native-contract-20261105.md` for the verified future extension seam.

On the cluster build node, verify a built image with
`python3 tests/lite/test_deepseek_startup.py <image>`. It starts a fresh non-root
container without networking or user storage, checks authenticated HTTP, then
checks plugin activation and disposal through live configuration edits.

Build both images from the repository root:

```bash
docker build -f deepseek-harness/Dockerfile.lite -t deepseek-harness-lite:local .
docker build -f deepseek-harness/Dockerfile.pro -t deepseek-harness:local .
```

For a release, resolve `WEBTOP_RUNTIME_BASE_IMAGE` to an immutable compatible
OpenClaw/Webtop digest and pass it explicitly to the Pro build. Record that
digest and final image digests with the source commit. The Pro image retains its
desktop supervisor and user directories; building it does not upgrade any
running OpenClaw pool. Both images contain `/opt/clawmanager-dsh/native-manifest.json`.

## Lite temporary files

The shared Lite manager mounts an instance-owned PVC directory at `/tmp`. Its
identity uses the runtime, user and instance IDs, so ordinary files and metadata
survive restarts and confirmed Pod migrations. DSH commands share that directory.
`/run` and `/dev/shm` are recreated; running processes, sockets and IPC state do
not migrate.

Existing pools built before this support still use tmpfs. Before updating those
pools, retain the old tmp directory through a supervisor file descriptor, stop
and confirm the old writer, then export its final files. Restore only while
holding the exact stopped generation's maintenance writer lease, and refuse to
overwrite existing files. Replacing the old Pod before exporting destroys tmpfs
contents.

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
