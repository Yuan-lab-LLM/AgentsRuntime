package openclaw

import (
	"fmt"
	"path/filepath"
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
	switch p.runtimeType {
	case "openclaw-shell":
		return "OpenClaw Shell"
	default:
		return "OpenClaw"
	}
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
	startupTimeout := 90 * time.Second
	if p.Type() == "openclaw" {
		startupTimeout = 5 * time.Minute
	}
	return gateway.RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/openclaw-agent",
		GatewayPortStart:      20000,
		GatewayPortEnd:        20099,
		GatewayPortBlockSize:  3,
		GatewayCapacity:       100,
		GatewayAuthMode:       "trusted-proxy",
		GatewayStartupTimeout: startupTimeout,
	}
}

func (p Profile) GatewayCommand(authMode string) []string {
	bind := "auto"
	if p.Type() == "openclaw" {
		// The isolated filesystem hides container markers, so auto falls back
		// to loopback and cannot accept the backend's Pod-IP proxy connection.
		bind = "lan"
	}
	return []string{"openclaw", "gateway", "run", "--allow-unconfigured", "--auth", authMode, "--bind", bind, "--force"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	return OpenClawGatewayEnv(base, cfg, req, workspacePath, port)
}

func (p Profile) PrepareWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspacePath {
		return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
	}
	return p.prepareTeamWorkspace(cfg, req, workspacePath)
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) error {
	return WriteGatewayConfig(cfg, req, workspacePath, port)
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return newHealthChecker(cfg)
}

func (p Profile) GatewayProcessCommand(command, env []string) []string {
	if p.Type() != "openclaw" {
		return command
	}
	return gateway.LiteTeamGatewayCommand(p.Type(), command, env)
}
func (p Profile) prepareTeamWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	if p.Type() != "openclaw" {
		return nil
	}
	return gateway.PrepareLiteTeamSharedWorkspace(cfg.WorkspaceRoot, req, workspace, filepath.Join(workspace, "home", ".openclaw", "workspace"))
}
