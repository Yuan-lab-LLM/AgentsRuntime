# Hermes Redis Team Plugin

Hermes gateway platform adapter for the ClawManager Redis Streams Team Bus.

This plugin is the Hermes-side counterpart of `plugins/openclaw-redis-team`.
It does not use the OpenClaw plugin SDK. Instead, it registers a Hermes
platform named `redis_team` and the shared team tools:

- `team_artifact_write`
- `team_artifact_read`
- `team_artifact_list`
- `team_artifact_mkdir`
- `team_artifact_preview`
- `team_send`
- `team_status`
- `team_update_progress`
- `team_complete_task`

## Runtime contract

The `active_context_delivery_v1` adapter uses the pinned native gateway's
explicit active-agent reader and `steer()` for a valid reminder to the current
busy work. It never changes global busy modes or `allow_gateway_control`.
Transport receipt, submission and inclusion in a prepared native request are
separate facts; inclusion does not guarantee model compliance. Identity and
version come from the existing business envelope, not new model parameters.
Ordinary peer/long context, idle recovery and legitimate post-delivery Leader
questions retain their original dispatch path. Connected presence explicitly
publishes online and its system generation after a restart.

Use `hermes/Dockerfile.team-release` only on a verified immutable native base;
it applies the narrow gateway reader patch and regenerates the image-owned
release inventory. `hermes/tests/smoke_team_context_delivery.py` exercises the
actual native steering boundary without claiming real-model acceptance.

The adapter reads the same Team env contract used by the OpenClaw runtime:

```text
CLAWMANAGER_TEAM_ENABLED=true
CLAWMANAGER_TEAM_ID=team_xxx
CLAWMANAGER_TEAM_MEMBER_ID=developer
CLAWMANAGER_TEAM_ROLE=developer
CLAWMANAGER_TEAM_REDIS_URL=redis://redis:6379/0
CLAWMANAGER_TEAM_SHARED_DIR=/team
```

Optional:

```text
CLAWMANAGER_TEAM_AUTORUN=true
CLAWMANAGER_TEAM_CONSUMER_GROUP=team-members
CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS=1800
CLAWMANAGER_TEAM_MANAGER_URL=http://clawmanager:8080
CLAWMANAGER_TEAM_TOKEN=...
CLAWMANAGER_TEAM_PREVIEW_ORIGIN=http://clawmanager-egress-proxy.<namespace>.svc.cluster.local:3128
```

## Redis keys

```text
claw:team:<teamId>:inbox:<memberId>
claw:team:<teamId>:events
claw:team:<teamId>:presence
claw:team:<teamId>:dlq
```

The adapter emits the current Worker-side protocol-v4 completion proposal and
waits briefly for ClawManager's completion acknowledgement. A substantive
Hermes final response is proposed automatically, while Monitor/status turns and
empty or question-only responses remain non-terminal. ClawManager remains the
authority that accepts or rejects completion.

Formal assignments use the same lifecycle projection as OpenClaw Lite:

```text
task_received -> task_started -> narrative/heartbeat -> completion_proposed -> completion acknowledgement
```

`inbound` is transport audit only. `task_received` and `task_started` are
hidden business-state events; narratives are visible but non-authoritative;
heartbeats and Monitor replies are hidden evidence and cannot complete or
reopen work. Redis delivery, an individual Hermes model turn, and the business
Assignment are tracked independently so a finished turn does not make a still
active Assignment appear idle. Only an accepted completion acknowledgement or
an explicit terminal failure ends the Assignment.

## Shared workspace ownership

ClawManager prepares the Team shared tree with a stable shared group and
setgid/group-write permissions. Each Worker keeps its own UID and uses that
shared GID. The Hermes adapter validates that existing shared directories are
readable and writable, but it never changes their mode or owner. It only applies
the cooperative mode to a directory it created itself. This is required for
NFS-backed Teams, where a Worker can create files through group access but
cannot `chmod` a directory owned by another runtime.

Private readiness and startup-failure files remain in the Worker-owned private
runtime directory and use restrictive permissions.

## AgentsRuntime packaging

`plugins/hermes-redis-team` is the canonical source and is copied directly into
the Hermes image. The historical `hermes/vendor-plugins/redis_team` directory is
not used by current image builds.

