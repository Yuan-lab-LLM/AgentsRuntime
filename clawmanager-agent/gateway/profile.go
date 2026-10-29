package gateway

import (
	"context"
	"fmt"
)

type RuntimeRegistrationMetadata struct {
	OpenClawVersion   string
	Capabilities      []string
	ProtocolVersion   string
	TeamPluginVersion string
	SessionStore      string
}

type RegistrationMetadataProvider interface {
	RegistrationMetadata(Config) RuntimeRegistrationMetadata
}
type GatewayRecoveryPolicy interface{ AutomaticGatewayRecovery(Config) bool }
type GatewayAutomationProvider interface {
	GatewayAutomations(Config, CreateGatewayRequest) func(context.Context, int) error
}

// GatewayAutomationExecutor runs a complete business command under the active
// gateway's ownership fence. Environment contains explicit per-command overrides.
type GatewayAutomationExecutor func(context.Context, []string, []string) ([]byte, error)

// ManagedGatewayAutomationProvider keeps native reconciliation outside core while
// requiring an owned command runner for isolated process lifecycles.
type ManagedGatewayAutomationProvider interface {
	ManagedGatewayAutomations(Config, CreateGatewayRequest, GatewayAutomationExecutor) func(context.Context, int) error
}

// GatewayCommandRunner joins a business command and confirms its full namespace
// teardown. Unconfirmed cleanup must wrap ErrGatewayStopFailed.
type GatewayCommandRunner interface {
	RunGatewayCommand(context.Context, GatewayStartSpec) ([]byte, error)
}

// ManagedCredentialMigrator is mandatory for protected native lifecycles.
// It runs after trusted networking and before any native preparation or backup.
type ManagedCredentialMigrator interface {
	MigrateManagedCredentials(context.Context, Config, CreateGatewayRequest, string) error
}

// Missing native adapters remain inert; production commands inject profiles.
type runtimeNeutralProfile struct{ runtimeType string }

func (p runtimeNeutralProfile) Type() string                 { return p.runtimeType }
func (p runtimeNeutralProfile) DisplayName() string          { return p.runtimeType }
func (runtimeNeutralProfile) Defaults() RuntimeDefaults      { return RuntimeDefaults{} }
func (runtimeNeutralProfile) GatewayCommand(string) []string { return nil }
func (runtimeNeutralProfile) GatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspace string, port int) []string {
	return GenericGatewayEnv(base, cfg, req, workspace, port)
}
func (runtimeNeutralProfile) PrepareWorkspace(cfg Config, req CreateGatewayRequest, workspace string) error {
	prepared, err := PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspace {
		return fmt.Errorf("%w: prepared %s want %s", ErrWorkspacePath, prepared, workspace)
	}
	return nil
}
func (runtimeNeutralProfile) WriteGatewayConfig(Config, CreateGatewayRequest, string, int) error {
	return nil
}
func (runtimeNeutralProfile) HealthChecker(cfg Config) GatewayHealthChecker {
	return NewHTTPGatewayHealthChecker(cfg)
}
