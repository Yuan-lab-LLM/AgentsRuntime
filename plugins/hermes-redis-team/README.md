# Hermes Redis Team Plugin

Hermes gateway platform adapter for the ClawManager Redis Streams Team Bus.

This plugin is the Hermes-side counterpart of `plugins/openclaw-redis-team`.
It does not use the OpenClaw plugin SDK. Instead, it registers a Hermes
platform named `redis_team` and the shared team tools:

- `team_send`
- `team_status`
- `team_update_progress`
- `team_complete_task`

## Runtime contract

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
```

## Redis keys

```text
claw:team:<teamId>:inbox:<memberId>
claw:team:<teamId>:events
claw:team:<teamId>:presence
claw:team:<teamId>:dlq
```

## AgentsRuntime packaging

`plugins/hermes-redis-team` is the canonical source. The Hermes image currently
builds with `hermes/` as its Docker context, so `hermes/vendor-plugins/redis_team`
is a build-context mirror of this directory.

