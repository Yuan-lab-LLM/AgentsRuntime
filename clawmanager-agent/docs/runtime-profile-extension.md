# Runtime Profile Extension Guide

This guide describes how to add a new runtime type to the shared `clawmanager-agent`.

## Architecture

The shared agent core owns lifecycle, control APIs, reporting, gateway process management, ports, metrics, skills, and workspace validation.

Runtime-specific behavior belongs in a runtime profile under:

```text
clawmanager-agent/internal/runtime/<runtime-type>/
```

A profile implements `gateway.RuntimeProfile`.

## RuntimeProfile Contract

```go
type RuntimeProfile interface {
    Type() string
    DisplayName() string
    Defaults() RuntimeDefaults
    GatewayCommand(authMode string) []string
    GatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string
    PrepareWorkspace(cfg Config, req CreateGatewayRequest, workspacePath string) error
    WriteGatewayConfig(cfg Config, req CreateGatewayRequest, workspacePath string) error
    HealthChecker(cfg Config) GatewayHealthChecker
}
```

Use profile methods for runtime-specific behavior only. Do not duplicate the agent loop, report client, control server, port allocator, gateway manager, or metrics collector.

## Config Precedence

Configuration is resolved in this order:

1. Platform-injected environment variables.
2. Selected runtime profile defaults.
3. Generic fallback defaults.

Unknown runtime types are allowed only when `RUNTIME_GATEWAY_COMMAND` is set.

## Shared LLM Configuration

Model environment precedence, JSON/scalar parsing, legacy compatibility,
`provider/model` normalization, deduplication, and provider grouping belong to
`internal/llmconfig`. Runtime profiles must consume that package and only render
the normalized settings into their native configuration format.

Container bootstrap scripts must not reimplement model parsing. Images that need
normalized settings before the runtime agent loop starts should invoke:

```text
clawmanager-agent llm-config --format canonical
```

Runtime-specific code remains responsible for schema details such as Hermes
YAML fields, OpenClaw reasoning compatibility, or OpenCode provider enablement.

## Required Runtime Decisions

Every new runtime profile must define:

- Runtime type string, such as `myruntime`.
- Display name.
- Workspace root default.
- Agent data directory default.
- Gateway port range and block size.
- Gateway capacity.
- Gateway auth mode.
- Default gateway command, or a decision to require `RUNTIME_GATEWAY_COMMAND`.
- Gateway environment variables.
- Workspace preparation behavior.
- Runtime config writer behavior.
- Startup health checker.

## Directory Conventions

Use this default workspace shape unless the platform has a runtime-specific reason to differ:

```text
${RUNTIME_WORKSPACE_ROOT}/<runtime-type>/user-<user-id>/instance-<instance-id>/
  home/
  skills/
```

Use a persistent agent data directory:

```text
${RUNTIME_AGENT_DATA_DIR}
```

Runtime profile code must reject workspace paths outside the configured workspace root.

## Profile Skeleton

```go
package myruntime

import (
    "fmt"
    "strings"
    "time"

    "github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

type Profile struct {
    runtimeType string
}

func NewProfile(runtimeType string) Profile {
    return Profile{runtimeType: strings.ToLower(strings.TrimSpace(runtimeType))}
}

func (p Profile) Type() string {
    return p.runtimeType
}

func (p Profile) DisplayName() string {
    return "My Runtime"
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
    return gateway.RuntimeDefaults{
        WorkspaceRoot:         "/workspaces",
        AgentDataDir:          "/var/lib/clawmanager-agent",
        GatewayPortStart:      20000,
        GatewayPortEnd:        20099,
        GatewayPortBlockSize:  1,
        GatewayCapacity:       100,
        GatewayAuthMode:       "trusted-proxy",
        GatewayStartupTimeout: 90 * time.Second,
    }
}

func (p Profile) GatewayCommand(authMode string) []string {
    return []string{"myruntime", "serve", "--auth", authMode}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
    return gateway.GenericGatewayEnv(base, cfg, req, workspacePath, port)
}

func (p Profile) PrepareWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
    prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
    if err != nil {
        return err
    }
    if prepared != workspacePath {
        return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
    }
    return nil
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
    return nil
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
    return gateway.NewNoopGatewayHealthChecker()
}
```

Register the profile in `clawmanager-agent/internal/agent/config.go`:

```go
_ = registry.Register(myruntime.NewProfile("myruntime"))
```

## Dockerfile Snippet

Runtime images should build from the repository root when they need the shared component:

```dockerfile
FROM golang:1.26-bookworm AS clawmanager-agent-builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src/clawmanager-agent
COPY clawmanager-agent ./
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -o /out/clawmanager-agent ./cmd/clawmanager-agent

FROM runtime-base
COPY --from=clawmanager-agent-builder /out/clawmanager-agent /usr/local/bin/clawmanager-agent
```

Use `.runtime-image.json` when CI discovery would otherwise choose the runtime directory as build context:

```json
{
  "runtime": "myruntime",
  "context": ".",
  "dockerfile": "./myruntime/Dockerfile"
}
```

## Tests

Add tests for:

- Profile type and defaults.
- Config loading by `CLAWMANAGER_RUNTIME_TYPE`.
- Unknown runtime fallback behavior.
- Gateway command normalization.
- Gateway env injection.
- Workspace path rejection.
- Runtime-specific config writer output.
- Health checker behavior.

Run:

```bash
go test ./...
```

## Acceptance Checklist

- The runtime has a profile package or uses the generic fallback with explicit `RUNTIME_GATEWAY_COMMAND`.
- The runtime image installs `/usr/local/bin/clawmanager-agent`.
- The image provides `CLAWMANAGER_RUNTIME_TYPE`.
- The image starts `clawmanager-agent` only when runtime-agent tokens are present.
- Profile tests pass.
- `go test ./...` passes under `clawmanager-agent/`.
- Docker build context can access `clawmanager-agent/`.
