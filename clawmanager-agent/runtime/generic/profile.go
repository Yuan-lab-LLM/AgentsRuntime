package generic

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
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
	if p.runtimeType == "" {
		return "Generic Runtime"
	}
	return p.runtimeType
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

func (p Profile) GatewayCommand(string) []string {
	return nil
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

func (p Profile) WriteGatewayConfig(gateway.Config, gateway.CreateGatewayRequest, string, int) error {
	return nil
}

func (p Profile) HealthChecker(gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewNoopGatewayHealthChecker()
}
