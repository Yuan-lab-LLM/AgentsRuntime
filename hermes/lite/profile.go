package hermes

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type Profile struct {
	runtimeType  string
	desktopWeb   bool
	capabilities *gateway.HealthCapabilities
}

func NewProfile(runtimeType string) Profile {
	return Profile{runtimeType: strings.ToLower(strings.TrimSpace(runtimeType)), desktopWeb: desktopWebEnabled()}
}

func (p Profile) Type() string {
	return p.runtimeType
}

func (p Profile) IsolatedGatewayLifecycle() bool { return p.desktopWeb }

func (p Profile) DisplayName() string {
	return "Hermes"
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
	if p.desktopWeb {
		return []string{"start-hermes-lite-runtime"}
	}
	return []string{"start-hermes-desktop"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	if p.desktopWeb {
		return desktopWebEnvironment(base, cfg, req, workspacePath, port)
	}
	env := append([]string(nil), base...)
	env = gateway.ApplyRequestEnvironment(env, req)
	env = gateway.ApplyLiteTeamConfigEnvironment(env, req, workspacePath)
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_GATEWAY_GENERATION", strconv.Itoa(req.Generation))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	env = setEnv(env, "HOME", path.Join(workspacePath, "home"))
	env = setEnv(env, "HERMES_HOME", path.Join(workspacePath, "home", ".hermes"))
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))
	env = setEnv(env, "HERMES_ACCEPT_HOOKS", "1")
	teamWorkerHome := path.Join(workspacePath, "home", ".clawmanager-team-worker")
	env = setEnv(env, "HERMES_TEAM_WORKER_HOME", teamWorkerHome)
	env = setEnv(
		env,
		"CLAWMANAGER_TEAM_READY_FILE",
		path.Join(teamWorkerHome, ".hermes", "runtime", "redis-team.ready.json"),
	)
	env = unsetEnv(
		env,
		desktopWebFlag,
		"RUNTIME_AGENT_CONTROL_TOKEN",
		"RUNTIME_AGENT_REPORT_TOKEN",
		"RUNTIME_AGENT_DATA_DIR",
		"RUNTIME_AGENT_PUBLIC_PORT",
		"RUNTIME_AGENT_LISTEN_ADDR",
	)
	if cfg.GatewayAuthMode == "trusted-proxy" {
		env = unsetEnv(env, "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")
	} else if cfg.GatewayToken != "" {
		env = setEnv(env, "RUNTIME_GATEWAY_TOKEN", cfg.GatewayToken)
	}
	return env
}

func (p Profile) PrepareWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	if p.desktopWeb {
		if err := validateDesktopWebRequest(cfg, req); err != nil {
			return err
		}
		if desktopWebTeamRequest(req) {
			prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
			if err != nil {
				return err
			}
			if prepared != workspacePath {
				return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
			}
		}
		if err := prepareTeamWorkspace(cfg, req, workspacePath); err != nil {
			return err
		}
		return prepareDesktopWebWorkspace(cfg, req, workspacePath)
	}
	prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspacePath {
		return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
	}
	return prepareTeamWorkspace(cfg, req, workspacePath)
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, _ int) error {
	return WriteGatewayConfig(cfg, req, workspacePath)
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return newHealthChecker(cfg)
}

func requestEnvValue(req gateway.CreateGatewayRequest, keys ...string) (string, bool) {
	return gateway.RequestEnvValue(req, keys...)
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

func unsetEnv(env []string, keys ...string) []string {
	remove := map[string]bool{}
	for _, key := range keys {
		remove[key+"="] = true
	}
	filtered := env[:0]
	for _, item := range env {
		keep := true
		for prefix := range remove {
			if strings.HasPrefix(item, prefix) {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (p Profile) GatewayProcessCommand(command, env []string) []string {
	return gateway.LiteTeamGatewayCommand(p.Type(), command, env)
}
