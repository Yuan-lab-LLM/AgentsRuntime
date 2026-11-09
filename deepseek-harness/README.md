# DeepSeek Harness Runtime

This directory owns the ClawManager runtime images for DeepSeek Harness.

- `Dockerfile.lite` builds the pooled, headless Lite runtime. The shared
  `clawmanager-agent` starts one isolated `dsh web` process per instance. DSH
  listens only on a private loopback port; the agent-facing gateway validates
  the ClawManager instance token and holds DSH's browser-session cookie inside
  the runtime process boundary.
- `Dockerfile.pro` builds the dedicated Webtop Pro runtime. DeepSeek Harness
  runs on loopback port `3080` and opens automatically in the desktop browser.

Both images pin `@deepseek-ai/dsh` and apply the managed Cordis provider patch
from `config/managed.cordis.patch.yml`. The Lite image also pins the `pnpm` CLI
required by dsh-market so pooled non-root instances never need to modify the
system Node installation at runtime. ClawManager supplies the OpenAI-compatible
base URL, instance credential, and allowed model list at runtime.

Lite also locks the Cordis core family in `cordis.lock.json`. Pinning the DSH
CLI alone permits incompatible transitive patch releases: newer HMR packages
remove `registerConfig`, and newer Loader packages change activation ordering.
The small official archives in `vendor/cordis` are checked against SHA256 and
registry SHA512 integrity before installation. DSH starts with Node's explicit
`--expose-internals` flag, which its HMR service requires. The Lite image
applies one SHA-pinned sandbox-local patch: workspace-write commands bind the
outer instance-private `/tmp` instead of creating another temporary filesystem.
The read-only profile and Landlock grants retain their existing constraints.

On the cluster build node, verify a built image with
`python3 tests/lite/test_deepseek_startup.py <image>`. It starts a fresh non-root
container without networking or user storage, checks authenticated HTTP, then
checks plugin activation and disposal through live configuration edits.

Build both images from the repository root:

```bash
docker build -f deepseek-harness/Dockerfile.lite -t deepseek-harness-lite:local .
docker build -f deepseek-harness/Dockerfile.pro -t deepseek-harness:local .
```

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
