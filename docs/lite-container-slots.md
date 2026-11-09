# Lite container slots implementation

The `container` branches of AgentsRuntime and ClawManager implement four slim
Lite runtime images, a shared Go management core, a trusted pool coordinator and
fixed single-instance containers. Enablement is explicit; existing deployments
remain on the legacy process protocol until migrated.

## Code map

| Responsibility | Location |
| --- | --- |
| Common assembly/config, existing Pro entry | `clawmanager-agent/app`, `agent`, `internal/instanceagent` |
| Shared gateway lifecycle/control/reporting | `clawmanager-agent/gateway`, `control` |
| Coordinator, slot identity, persistent fencing, metrics | `clawmanager-agent/slots` |
| Linux process/filesystem/syscall isolation | `clawmanager-agent/sandbox` |
| Protected per-execution network, relay and scoped brokers | `clawmanager-agent/netguard`, `netrelay`, `netbroker`, `slots` |
| Bounded pre-exec managed credential migration | `clawmanager-agent/managedconfig`, `<runtime>/lite` |
| Bounded privileged workspace operations | `clawmanager-agent/workspaceio` |
| Native runtime profiles/config/env/health/Team | `<runtime>/lite` |
| OpenClaw native upgrade engine | `openclaw/lite/upgrade*.go` |
| Historical all-runtime assembly for Pro/Shell | `legacy-agent` |
| Per-runtime build definitions | `<runtime>/Dockerfile.lite` |

Shared code receives runtime-specific behavior through injected profiles and the
native upgrade interface. Existing public management payloads retain their wire
names for compatibility. Historical Pro instance serializers still have their
compatibility packages in the common module; Lite native profiles and the
OpenClaw migration engine live in their respective runtime module.

## Boundaries and ownership

Only the trusted supervisor sees the PVC pool root, management sockets, state
books, writer ledgers, report/control credentials and upgrade leases. A gateway
and every descendant receive an immutable image allowlist, its own instance
mount and explicitly authorized Team mounts. The sandbox starts after switching
to its assigned outer UID/GID, creates private user/PID/IPC/UTS/mount namespaces,
drops capabilities and receives an explicit environment. No whole-root or
whole-PVC fallback exists. Business `/tmp` binds only its instance-owned PVC
leaf; reusing a slot for another instance never grants the previous instance's
files. `/run` and `/dev/shm` get fresh tmpfs mounts on every execution.

The supervisor derives the tmp source as
`<WorkspaceRoot>/.clawmanager-instance-tmp/<runtime>/user-N/instance-N/tmp`.
Generation, slot and Pod identity do not change this path. Protected 0711
ancestors stay outside business mounts; only the sticky 01777 instance leaf,
owned by the assigned business UID/GID, appears as `/tmp`. Symlinks, unsafe
ownership or permissions block startup, with no ephemeral fallback. Existing
files are never recursively changed or deleted. Main and auxiliary business
commands share this leaf. Standalone/upgrade sandboxes without an instance tmp
grant retain ephemeral scratch.

With `RUNTIME_NETWORK_ISOLATION=true`, each execution also gets a private
network namespace, including localhost and abstract sockets. A credential-free
helper forwards through fixed parent-owned brokers; native ingress requires a
current instance credential before reaching a fixed private listener. Missing
policy, identity, lease or verified cleanup blocks execution or slot reuse.
Without that opt-in, the legacy slot profile continues to share Pod networking.

The supervisor uses root-bounded file operations before launch as well: user
symlinks cannot redirect managed configuration, plugin preparation, cron
publication or native upgrade file operations to siblings. Root-owned fixed
management ancestors remain unwritable by tenants. A native upgrade command
gets only its own workspace and operation-specific probe/scratch; readonly
preflights have a readonly live workspace. Traversable management ancestors are
0711 and never mounted, while credentials/leases remain private.

`Track` supplies controlled startup/identity FDs and an architecture-checked
seccomp policy to bubblewrap. The policy rejects privileged mount/namespace,
ptrace/process-memory, kernel-module/keyring and similar operations, blocks
namespace clone flags, and returns ENOSYS for clone3 so normal libc threads can
fall back to clone. See the [Linux seccomp documentation](https://docs.kernel.org/userspace-api/seccomp_filter.html)
and [bubblewrap's FD policy interface](https://github.com/containers/bubblewrap/blob/main/bwrap.xml).
The supported image architectures are Linux amd64 and arm64. Real kernel/browser
compatibility is a separate release gate.

Business listener readiness checks attribute sockets to the recorded PID
namespace before credential-bearing health requests. This protects current
listener ownership and complements the protected private network boundary; it
does not replace business protocol authentication.

The backend authenticates the management server using TLS 1.3 and a private
namespace CA with a fixed DNS identity. Only coordinators mount the server key;
slots use hidden local Unix sockets. Clients reject plaintext and redirects,
including inherited upgrade/control operations. Expired or inconsistent TLS
material requires a controlled rotation, never insecure verification fallback.

Instance creation persists ConfigMap desired state and generation-specific
immutable business Secrets first. Successful create acknowledgements mark that
generation applied, so periodic recovery does not replay every healthy instance.
Pending recovery uses bounded workers, resumes after timeouts and isolates
individual malformed records.

Stop success requires the matching namespace init and all descendants to have
exited, with a graceful application signal before forced teardown. A shared
per-instance lifetime flock and durable generation ledger remain outside user
mounts. Missing/unknown source stop never authorizes migration or slot reuse.
Native mutating upgrade operations use the same writer fence. Unconfirmed
maintenance cleanup leaves a durable `upgrading` quarantine, including after a
supervisor crash; elapsed leases do not override that state.

Local PVC is supported only in single-node clusters. Multiple nodes require
actual Longhorn CSI plus RWX on PVC and PV. Migration is stop/start against the
shared PVC, without a data-copy phase. Automatic fencing/recovery of an unknown
old writer is intentionally not implemented; it remains blocked for external
reconciliation rather than guessing from a timeout.

This is file persistence and cold restart, without process-memory migration.
Ordinary `/tmp` files and metadata survive; `/run`, `/dev/shm`, active sockets,
locks, PIDs and connections must be rebuilt. Do not treat persistent socket/PID
files as live runtime state. Keep ephemeral IPC in `/run` where possible.
Existing running instances still have their old tmpfs: export any needed files
before stopping them for upgrade, since the new image cannot recover destroyed
tmpfs contents. Existing tmp-leaf ownership/permission problems require explicit
offline review rather than automatic repair.

The opt-in Linux regression `TestLinuxPersistentTmpAcrossPods` in
`clawmanager-agent/slots/persistent_tmp_acceptance_linux_test.go` uses disposable
PVC storage and the real Worker/bubblewrap lifecycle. Run phases `source`,
`reuse-B`, then `target` with `CM_PERSISTENT_TMP_ACCEPTANCE=1`,
`CM_PERSISTENT_TMP_ROOT=/workspaces` for the shared trusted PVC root,
`CM_PERSISTENT_TMP_PHASE` set to the phase, and
`CM_PERSISTENT_TMP_POD_UID` set to the actual current Pod UID. The first two
phases run in the same source Pod/slot; `target` runs in a different Pod sharing
the PVC. Use a root supervisor and install the test binary under `/usr/local/bin/`
in dedicated fixtures only. A single-node local-PVC run verifies cross-Pod file
continuity, not multi-node Longhorn migration.

## Protected network and credential profile

Protected mode requires a supported modern user-namespace-capable Kubernetes
runtime and explicitly configured Pod/Service address ranges. Only trusted
supervisors receive namespace/network administration capabilities inside the
Pod user namespace. Business processes and the relay helper have no effective
capabilities. This remains a shared-kernel design, not a microVM boundary.

Management control/report/signing credentials stay outside business mounts.
Local brokers bind the current instance, user, generation and actual Pod UID.
They supply model keys and managed Browser/Team credentials at trusted edges;
old CM-managed generated credentials are migrated before native preparation.
Independent user provider settings remain user-owned. Unknown historical copies
and externally exposed Redis credentials require separate inventory/retirement.
Protected Teams require cluster-confined Redis and migration readiness for the
entire active cohort before retiring legacy preview grants.

Stopped OpenClaw maintenance runs in a separate offline network namespace with
only its authorized workspace and operation capsule. Read-only preflight keeps
the live workspace read-only. Unconfirmed process cleanup retains its fence.

Slot count is configurable, not fixed at eight. Configure Pod resources and
slot requests/limits together; one coordinator also consumes resources. CPU and
memory requests are scheduling reservations, not guaranteed spare capacity.
A Pod memory ceiling does not eliminate individual container OOM limits.

## Verification and enablement

Image commands are in [lite-images.md](lite-images.md). ClawManager's
`deployments/k8s/lite-slots/README.md` documents configuration, storage checks,
maintenance-window conversion from old pools and the backend patch example.
Neither repository's default deployment is automatically switched to slots.

Portable test evidence covers shared lifecycle/isolation contracts, filesystem
symlink boundaries, native profile compatibility, assignment CAS/identity,
ConfigMap/Secret separation, recovery fairness, strict stop receipts and
cross-Pod handover ordering using fake transports. Linux binaries and sandbox
tests are cross-compiled for the supported architectures. Build contracts,
entrypoints and locked Hermes payload verification run locally.

On 2026-10-17, the backend and all four linux/amd64 Lite images were built and
pushed on target node12, with all image smoke checks passing. The shared Linux
suite and both namespace/seccomp/UID/signal acceptance modes passed in direct
Docker with explicit systempaths=unconfined. A real pool+slot management test
passed TLS/authentication/UDS/metrics and a no-model fixture gateway lifecycle.
Longhorn RWX cross-node flock, atomic replacement and fsync checks also passed.
Release evidence is under `/root/cm-container-slots-20261017` on node12.

Production Kubernetes conversion remains blocked: the cluster's customized
v1.20.5 dockershim converts empty proc path lists to nil, and Docker restores its
default masks. Thus the actual Kubernetes sandbox gate fails even with the
admitted Unmasked field. A container-only compatibility patch has a passing
protobuf/JSON regression proof, but replacing the customized kubelet requires
its matching source and build procedure. Existing business instances stay on
the old pools; no unknown writer is assumed stopped. Direct Docker evidence is
not a passing Kubernetes gate.

Real model/browser/Team workflows, cross-node instance migration and fault
injection, arm64 execution and resource-density acceptance remain separate
gates. Image sizes are measured; a slim base is not evidence of lower running
memory. Full backend Linux compilation and config/services/k8s/services tests
now pass on node12; earlier local proxy/listener restrictions do not represent
those remote results.

The network/credential enhancement has a separate server54 canary on 2026-10-21.
Common real-kernel isolation and installed OpenClaw offline maintenance passed;
four native startup/UI, Browser/CNI, Team and failure-injection evidence is
tracked in the network-credentials implementation ledger. Earlier node12
results above are historical and do not certify this newer profile. No claim
of cross-node Longhorn execution on the single-node server54 canary is made.
