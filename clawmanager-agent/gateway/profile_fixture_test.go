package gateway

import (
	"fmt"
	"time"
)

type openClawCompatProfile struct {
}

func (openClawCompatProfile) Type() string {
	return "openclaw"
}

func (openClawCompatProfile) DisplayName() string {
	return "OpenClaw"
}

func (openClawCompatProfile) Defaults() RuntimeDefaults {
	return RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/openclaw-agent",
		GatewayPortStart:      20000,
		GatewayPortEnd:        20099,
		GatewayPortBlockSize:  3,
		GatewayCapacity:       100,
		GatewayAuthMode:       "trusted-proxy",
		GatewayStartupTimeout: 90 * time.Second,
	}
}

func (openClawCompatProfile) GatewayCommand(authMode string) []string {
	return []string{"openclaw", "gateway", "run", "--allow-unconfigured", "--auth", authMode, "--bind", "auto", "--force"}
}

func (openClawCompatProfile) GatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string {
	return GenericGatewayEnv(base, cfg, req, workspacePath, port)
}

func (openClawCompatProfile) PrepareWorkspace(cfg Config, req CreateGatewayRequest, workspacePath string) error {
	prepared, err := PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspacePath {
		return fmt.Errorf("%w: prepared %s want %s", ErrWorkspacePath, prepared, workspacePath)
	}
	return nil
}

func (openClawCompatProfile) WriteGatewayConfig(cfg Config, req CreateGatewayRequest, workspacePath string, port int) error {
	return nil
}

func (openClawCompatProfile) HealthChecker(cfg Config) GatewayHealthChecker {
	return NewHTTPGatewayHealthChecker(cfg)
}
