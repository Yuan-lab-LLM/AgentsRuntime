package windowsvm

import (
	"fmt"
	"path"
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
	return "Windows VM"
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
	return gateway.RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/clawmanager-agent",
		GatewayPortStart:      8006,
		GatewayPortEnd:        8006,
		GatewayPortBlockSize:  1,
		GatewayCapacity:       1,
		GatewayAuthMode:       "trusted-proxy",
		GatewayStartupTimeout: 5 * time.Minute,
	}
}

func (p Profile) GatewayCommand(string) []string {
	return []string{"/usr/local/bin/windows-vm-gateway"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	env := gateway.GenericGatewayEnv(base, cfg, req, workspacePath, port)
	env = setEnv(env, "WINDOWS_VM_STORAGE_DIR", path.Join(workspacePath, "storage"))
	env = setEnv(env, "WINDOWS_VM_SHARED_DIR", path.Join(workspacePath, "shared"))
	env = setEnv(env, "WINDOWS_VM_WEB_PORT", "8006")
	env = setEnv(env, "WINDOWS_VM_RDP_PORT", "3389")
	return env
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

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewHTTPGatewayHealthChecker(cfg)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for index, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
