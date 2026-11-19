package opencode

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
	return "OpenCode"
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
	return gateway.RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/clawmanager-agent",
		GatewayPortStart:      20000,
		GatewayPortEnd:        20299,
		GatewayPortBlockSize:  1,
		GatewayCapacity:       100,
		GatewayAuthMode:       "token",
		GatewayStartupTimeout: 90 * time.Second,
	}
}

func (p Profile) GatewayCommand(string) []string {
	return []string{"start-opencode-web"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = gateway.ApplyRequestEnvironment(env, req)
	env = unsetEnv(env, "CLAWMANAGER_DEFAULT_PROJECT_PATH", "CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH")
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_GATEWAY_GENERATION", strconv.Itoa(req.Generation))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_PROJECT_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	// ClawManager's workspace browser exposes workspacePath as its root. Keep
	// OpenCode's idea of the user's home aligned with that root as well, or a
	// project selected as "mcpclient" is resolved as workspacePath/home/mcpclient
	// even though uploads are stored at workspacePath/mcpclient.
	//
	// OpenCode's managed configuration and XDG state remain under the dedicated
	// home directory below so changing the browsing root does not move or expose
	// application state in the workspace browser.
	homePath := path.Join(workspacePath, "home")
	env = setEnv(env, "HOME", workspacePath)
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))

	opencodeHome := path.Join(homePath, ".opencode")
	configPath := path.Join(opencodeHome, "opencode.json")
	env = setEnv(env, "OPENCODE_CONFIG_DIR", opencodeHome)
	env = setEnv(env, "OPENCODE_CONFIG", configPath)
	env = setEnv(env, "XDG_CONFIG_HOME", path.Join(homePath, ".config"))
	env = setEnv(env, "XDG_CACHE_HOME", path.Join(homePath, ".cache"))
	env = setEnv(env, "XDG_DATA_HOME", path.Join(homePath, ".local", "share"))
	env = setEnv(env, "XDG_STATE_HOME", path.Join(homePath, ".local", "state"))
	// OpenCode 1.18.x's FFF backend refuses to search a directory that is also
	// HOME (and can return an empty project picker). A managed Lite workspace is
	// already isolated per instance, so use the ripgrep fallback. This keeps the
	// upload root and OpenCode's project picker root identical without enabling
	// expensive cross-home or filesystem-root indexing.
	env = setEnv(env, "OPENCODE_DISABLE_FFF", "1")

	username, password := resolveOpenCodeServerAuth(cfg, req)
	env = setEnv(env, "OPENCODE_SERVER_USERNAME", username)
	env = setEnv(env, "OPENCODE_SERVER_PASSWORD", password)

	env = unsetEnv(
		env,
		"RUNTIME_AGENT_CONTROL_TOKEN",
		"RUNTIME_AGENT_REPORT_TOKEN",
		"RUNTIME_AGENT_DATA_DIR",
		"RUNTIME_AGENT_PUBLIC_PORT",
		"RUNTIME_AGENT_LISTEN_ADDR",
	)
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
	opencodeHome := filepath.Join(workspacePath, "home", ".opencode")
	if err := os.MkdirAll(opencodeHome, 0o750); err != nil {
		return fmt.Errorf("create opencode home: %w", err)
	}
	if err := gateway.ChownWorkspace(opencodeHome, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown opencode home: %w", err)
	}
	return nil
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, _ int) error {
	return WriteGatewayConfig(cfg, req, workspacePath)
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return newHealthChecker(cfg)
}

func resolveOpenCodeServerAuth(cfg gateway.Config, req gateway.CreateGatewayRequest) (string, string) {
	username := "opencode"
	if value, ok := requestEnvValue(req, "OPENCODE_SERVER_USERNAME"); ok && strings.TrimSpace(value) != "" {
		username = strings.TrimSpace(value)
	}

	password := strings.TrimSpace(cfg.GatewayToken)
	if password == "" {
		if value, ok := requestEnvValue(
			req,
			"OPENCODE_SERVER_PASSWORD",
			"CLAWMANAGER_INSTANCE_ACCESS_TOKEN",
			"CLAWMANAGER_INSTANCE_TOKEN",
			"CLAWMANAGER_LLM_API_KEY",
			"OPENAI_API_KEY",
			"CLAWMANAGER_GATEWAY_TOKEN",
		); ok {
			password = strings.TrimSpace(value)
		}
	}
	return username, password
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
