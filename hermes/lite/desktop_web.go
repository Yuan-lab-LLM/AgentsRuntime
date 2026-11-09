package hermes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

const desktopWebFlag = "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED"

// This is a deployment switch, never a create-request or renderer capability.
func desktopWebEnabled() bool { return os.Getenv(desktopWebFlag) == "true" }

func validateDesktopWebRequest(cfg gateway.Config, req gateway.CreateGatewayRequest) error {
	mode := strings.TrimSpace(os.Getenv("CLAWMANAGER_HERMES_BACKEND_MODE"))
	if mode != "" && mode != "serve" {
		return fmt.Errorf("unsupported_hermes_protocol: only headless serve mode is verified")
	}
	if req.InstanceID <= 0 || req.UserID <= 0 || req.Generation <= 0 || req.UID <= 0 || req.GID <= 0 {
		return fmt.Errorf("invalid_gateway_identity: positive instance, user, generation, uid and gid are required")
	}
	// Linux credentials are uint32; reject truncation to root and the special
	// all-ones identity before chown or process creation.
	if uint64(req.UID) >= 1<<32-1 || uint64(req.GID) >= 1<<32-1 {
		return fmt.Errorf("invalid_gateway_identity: uid and gid exceed the supported range")
	}
	if cfg.GatewayPortBlockSize > 1 {
		return fmt.Errorf("port_conflict: Hermes Desktop Web requires one assigned port")
	}
	if req.GatewayPort != 0 && (req.GatewayPort < cfg.GatewayPortStart || req.GatewayPort > cfg.GatewayPortEnd) {
		return fmt.Errorf("port_conflict: assigned port is outside the configured pool")
	}
	if req.PortRange.Start != 0 || req.PortRange.End != 0 {
		if req.PortRange.Start < cfg.GatewayPortStart || req.PortRange.End > cfg.GatewayPortEnd || req.PortRange.End < req.PortRange.Start {
			return fmt.Errorf("port_conflict: requested range is outside the configured pool")
		}
	}
	_, _, err := desktopWebCredentials(cfg, req)
	return err
}

func desktopWebCredentials(cfg gateway.Config, req gateway.CreateGatewayRequest) (string, string, error) {
	if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		username, _ := requestEnvValue(req, "HERMES_DASHBOARD_BASIC_AUTH_USERNAME")
		if username == "" {
			username = "clawmanager"
		}
		password, _ := requestEnvValue(req, "CLAWMANAGER_INSTANCE_TOKEN")
		if strings.TrimSpace(password) == "" || strings.ContainsAny(username+password, "\r\n\x00") || strings.Contains(username, ":") {
			return "", "", fmt.Errorf("dashboard_auth_failed: managed instance credentials are required")
		}
		return username, password, nil
	}
	return dashboardCredentials(req)
}

func dashboardCredentials(req gateway.CreateGatewayRequest) (string, string, error) {
	username, _ := requestEnvValue(req, "HERMES_DASHBOARD_BASIC_AUTH_USERNAME")
	if username == "" {
		username = "clawmanager"
	}
	password, _ := requestEnvValue(req, "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD", "CLAWMANAGER_DASHBOARD_BASIC_AUTH_PASSWORD", "CLAWMANAGER_INSTANCE_ACCESS_TOKEN", "CLAWMANAGER_INSTANCE_TOKEN")
	if strings.TrimSpace(password) == "" || strings.ContainsAny(username+password, "\r\n\x00") || strings.Contains(username, ":") {
		return "", "", fmt.Errorf("dashboard_auth_failed: managed instance credentials are required")
	}
	return username, password, nil
}

func dashboardSigningSecret(password string) string {
	digest := sha256.Sum256([]byte("clawmanager-hermes-dashboard-session-v1\x00" + password))
	return hex.EncodeToString(digest[:])
}

func desktopWebEnvironment(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string, port int) []string {
	var env []string
	if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		env = setEnv(env, "CLAWMANAGER_MANAGED_SERVICES_URL", managedconfig.ServicesURL)
		env = setEnv(env, "CLAWMANAGER_NETWORK_ISOLATED", "1")
	}
	// Pod credentials, Python/Node injection switches, global HOME and provider
	// keys are deliberately absent. The image owns PATH; requests cannot set it.
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value := envValue(base, key); value != "" {
			env = setEnv(env, key, value)
		}
	}
	for _, key := range []string{"CLAWMANAGER_LLM_MODEL", "CLAWMANAGER_LLM_PROVIDER", "OPENAI_MODEL", "CLAWMANAGER_INSTANCE_TOKEN"} {
		if value, ok := requestEnvValue(req, key); ok {
			env = setEnv(env, key, value)
		}
	}
	if resolved, err := desktopWebLLMConfig(cfg, req); err == nil {
		for _, key := range []string{"CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"} {
			env = setEnv(env, key, resolved.LLMAPIKey)
		}
		for _, key := range []string{"CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "CUSTOM_BASE_URL"} {
			env = setEnv(env, key, resolved.LLMBaseURL)
		}
	}
	if username, password, err := desktopWebCredentials(cfg, req); err == nil {
		env = setEnv(env, "HERMES_DASHBOARD_BASIC_AUTH_USERNAME", username)
		env = setEnv(env, "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD", password)
		// Override invalid legacy persisted secrets without sharing signing keys
		// between managed instances or exposing pod credentials.
		env = setEnv(env, "HERMES_DASHBOARD_BASIC_AUTH_SECRET", dashboardSigningSecret(password))
	}
	if origin, proxies, err := desktopWebProxyConfig(cfg, req); err == nil {
		env = setEnv(env, "CLAWMANAGER_CONTROL_UI_ORIGIN", origin)
		env = setEnv(env, "CLAWMANAGER_TRUSTED_PROXY_CIDRS", strings.Join(proxies, ","))
	}
	if cfg.ProxyPolicyFile != "" {
		env = setEnv(env, "CLAWMANAGER_PROXY_POLICY_FILE", cfg.ProxyPolicyFile)
		env = setEnv(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE", cfg.Namespace)
	}
	values := map[string]string{
		desktopWebFlag: "true", "CLAWMANAGER_HERMES_BACKEND_MODE": "serve",
		"CLAWMANAGER_INSTANCE_ID": strconv.Itoa(req.InstanceID), "CLAWMANAGER_GATEWAY_GENERATION": strconv.Itoa(req.Generation),
		"CLAWMANAGER_USER_ID": strconv.Itoa(req.UserID), "CLAWMANAGER_RUNTIME_TYPE": "hermes",
		"CLAWMANAGER_WORKSPACE_PATH": workspace, "CLAWMANAGER_GATEWAY_PORT": strconv.Itoa(port),
		"HOME": filepath.Join(workspace, "home"), "HERMES_HOME": filepath.Join(workspace, "home", ".hermes"),
		"HOST": "0.0.0.0", "PORT": strconv.Itoa(port), "HERMES_ACCEPT_HOOKS": "0",
		"XDG_CACHE_HOME":  filepath.Join(workspace, "home", ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(workspace, "home", ".config"),
		"XDG_DATA_HOME":   filepath.Join(workspace, "home", ".local", "share"),
	}
	for key, value := range values {
		env = setEnv(env, key, value)
	}
	if desktopWebTeamRequest(req) {
		env = desktopWebTeamEnvironment(env, req, workspace)
	}
	return env
}

func desktopWebTeamRequest(req gateway.CreateGatewayRequest) bool {
	enabled, _ := requestEnvValue(req, "CLAWMANAGER_TEAM_ENABLED")
	if truthy(enabled) {
		return true
	}
	configJSON, _ := requestEnvValue(req, "CLAWMANAGER_TEAM_CONFIG_JSON")
	return strings.TrimSpace(configJSON) != ""
}

// desktopWebTeamEnvironment carries only the existing, reviewed Hermes Team
// contract into the co-hosted Team consumer. Dashboard credentials and the
// runtime-pod control plane remain isolated from the request environment.
func desktopWebTeamEnvironment(env []string, req gateway.CreateGatewayRequest, workspace string) []string {
	teamKeys := []string{
		"CLAWMANAGER_TEAM_ENABLED", "CLAWMANAGER_TEAM_ID", "CLAWMANAGER_TEAM_MEMBER_ID",
		"CLAWMANAGER_TEAM_ROLE", "CLAWMANAGER_TEAM_EFFECTIVE_ROLE", "CLAWMANAGER_TEAM_RUNTIME_TYPE",
		"CLAWMANAGER_TEAM_PROTOCOL_VERSION", "CLAWMANAGER_TEAM_COMMUNICATION_MODE",
		"CLAWMANAGER_TEAM_AUTORUN", "CLAWMANAGER_TEAM_CONSUMER_GROUP",
		"CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS", "CLAWMANAGER_TEAM_TASK_STALE_SECONDS",
		"CLAWMANAGER_TEAM_REDIS_URL", "CLAWMANAGER_TEAM_REDIS_DB", "CLAWMANAGER_TEAM_REDIS_PORT",
		"CLAWMANAGER_TEAM_REDIS_SERVICE", "CLAWMANAGER_TEAM_REDIS_SERVICE_NAME",
		"CLAWMANAGER_TEAM_REDIS_SERVICE_PORT", "CLAWMANAGER_TEAM_INBOX_KEY",
		"CLAWMANAGER_TEAM_EVENTS_KEY", "CLAWMANAGER_TEAM_PRESENCE_KEY", "CLAWMANAGER_TEAM_DLQ_KEY",
		"CLAWMANAGER_TEAM_MANAGER_URL", "CLAWMANAGER_TEAM_MANAGER_BASE_URL", "CLAWMANAGER_TEAM_TOKEN",
		"CLAWMANAGER_TEAM_PREVIEW_ORIGIN", "CLAWMANAGER_TEAM_COLLABORATION_POLICY_JSON",
		"CLAWMANAGER_TEAM_PROFILE_KEY", "CLAWMANAGER_TEAM_PROFILE_NAME",
		"CLAWMANAGER_TEAM_MEMBER_DESCRIPTION", "CLAWMANAGER_TEAM_SYSTEM_PROMPT",
		"CLAWMANAGER_TEAM_BACKEND_BOOTSTRAP", "CLAWMANAGER_TEAM_UMASK",
		"HERMES_AGENT_HELP_GUIDANCE", "CLAWMANAGER_BROWSER_PROXY_URL",
	}
	for _, key := range teamKeys {
		if value, ok := requestEnvValue(req, key); ok {
			env = setEnv(env, key, value)
		}
	}
	env = gateway.ApplyLiteTeamConfigEnvironment(env, req, workspace)
	env = setEnv(env, "CLAWMANAGER_TEAM_ENABLED", "true")
	workerHome := filepath.Join(workspace, "home", ".clawmanager-team-worker")
	env = setEnv(env, "HERMES_TEAM_WORKER_HOME", workerHome)
	env = setEnv(env, "CLAWMANAGER_TEAM_READY_FILE", filepath.Join(workerHome, ".hermes", "runtime", "redis-team.ready.json"))
	env = setEnv(env, "HERMES_ACCEPT_HOOKS", "1")
	return env
}

func prepareDesktopWebWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	for _, part := range strings.FieldsFunc(req.WorkspacePath, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return gateway.ErrWorkspacePath
		}
	}
	expected, err := gateway.ValidateWorkspacePath(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil || expected != workspace {
		return gateway.ErrWorkspacePath
	}
	// Use the writer's directory-handle walk so preparation applies the same
	// identity check before ownership changes, including on older workspaces.
	root, err := openDesktopWebHome(cfg, req, workspace)
	if err != nil {
		return err
	}
	return root.Close()
}
