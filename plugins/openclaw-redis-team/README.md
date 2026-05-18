# OpenClaw Redis Team Plugin

This plugin connects an OpenClaw runtime managed by ClawManager to a Redis Streams based Team Bus.

## Capabilities

- Starts a background Redis Streams consumer when Team env is present.
- Exposes `team_send` for assigning work to another team member.
- Exposes `team_status` for reading member status snapshots from the shared Team directory.
- Exposes `team_update_progress` and `team_complete_task` for structured progress/result reporting.
- Writes small events to Redis and writes durable task/status/result files under the shared Team directory.
- Attempts to run inbound tasks through OpenClaw embedded agent runtime helper when available.

## Required environment

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

When autorun is enabled, inbound Redis tasks are started through OpenClaw's
embedded agent helper. The plugin passes the configured
`agents.defaults.model.primary` selection, such as `auto/auto`, into the
embedded run so ClawManager-injected gateway URL and token settings are reused.
It reads the current runtime config through `api.runtime.config.current()` and
passes that config into `runEmbeddedAgent`, so embedded auth sees the same
`models.providers.auto.apiKey` as the OpenClaw gateway.
The plugin emits `task_failed` if the helper is unavailable or does not return
before `CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS`, so ClawManager does not leave
the task in `running` forever.

## Redis keys

```text
claw:team:<teamId>:inbox:<memberId>
claw:team:<teamId>:events
claw:team:<teamId>:presence
claw:team:<teamId>:dlq
```

## Packaging for AgentsRuntime

```bash
npm run build
npm pack --pack-destination ../../openclaw/vendor-plugins
```

Dockerfile install:

```dockerfile
COPY vendor-plugins/openclaw-redis-team.tgz /tmp/openclaw-redis-team.tgz
RUN HOME=/defaults openclaw plugins install /tmp/openclaw-redis-team.tgz
```
