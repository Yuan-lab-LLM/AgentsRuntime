package openclaw

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

type Profile struct {
	runtimeType string
}

func (p Profile) WritesStartupModelCatalog() bool { return true }

func NewProfile(runtimeType string) Profile {
	return Profile{runtimeType: strings.ToLower(strings.TrimSpace(runtimeType))}
}

func (p Profile) Type() string {
	return p.runtimeType
}

func (p Profile) DisplayName() string {
	switch p.runtimeType {
	case "openclaw-shell":
		return "OpenClaw Shell"
	default:
		return "OpenClaw"
	}
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
	return gateway.RuntimeDefaults{
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

func (p Profile) GatewayCommand(authMode string) []string {
	return []string{"openclaw", "gateway", "run", "--allow-unconfigured", "--auth", authMode, "--bind", "auto", "--force"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	return gateway.OpenClawGatewayEnv(base, cfg, req, workspacePath, port)
}

func (p Profile) PrepareWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	if err := gateway.CheckOpenClawTaskActivation(context.Background(), gateway.ApplyRequestEnvironment(os.Environ(), req)); err != nil {
		return err
	}
	if err := gateway.PrepareOpenClawReceiptDirectory(cfg, req); err != nil {
		return err
	}
	prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspacePath {
		return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
	}
	return nil
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) error {
	return WriteGatewayConfig(cfg, req, workspacePath, port)
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewHTTPGatewayHealthChecker(cfg)
}
