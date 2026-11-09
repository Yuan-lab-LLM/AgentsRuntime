package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"path/filepath"
	"strconv"
	"strings"
)

func OpenClawGatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = gateway.ApplyRequestEnvironment(env, req)
	// Control-plane policy always overrides untrusted instance environment.
	env = gateway.RemoveEnvironmentValues(env, "CLAWMANAGER_PROXY_POLICY_FILE")
	env = gateway.RemoveEnvironmentValues(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE")
	if cfg.ProxyPolicyFile != "" {
		env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_PROXY_POLICY_FILE", cfg.ProxyPolicyFile)
		env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE", cfg.Namespace)
	}
	if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		env = gateway.SetEnvironmentValue(env, "OPENCLAW_STATE_DIR", filepath.Join(workspacePath, "home", ".openclaw"))
		env = gateway.SetEnvironmentValue(env, "OPENCLAW_CONFIG_PATH", filepath.Join(workspacePath, "home", ".openclaw", "openclaw.json"))
	}
	if cfg.RuntimeType == "openclaw" {
		env = gateway.ApplyLiteTeamConfigEnvironment(env, req, workspacePath)
	}
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_AGENT_PERSISTENT_DIR", filepath.Join(workspacePath, "home", ".openclaw"))
	env = gateway.SetEnvironmentValue(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	env = gateway.SetEnvironmentValue(env, "HOME", filepath.Join(workspacePath, "home"))
	env = gateway.SetEnvironmentValue(env, "HOST", "0.0.0.0")
	env = gateway.SetEnvironmentValue(env, "PORT", strconv.Itoa(port))
	env = gateway.SetEnvironmentValue(env, "OPENCLAW_HOST", "0.0.0.0")
	env = gateway.SetEnvironmentValue(env, "OPENCLAW_PORT", strconv.Itoa(port))
	env = gateway.SetEnvironmentValue(env, "OPENCLAW_GATEWAY_PORT", strconv.Itoa(port))
	if cfg.GatewayAuthMode == "trusted-proxy" {
		env = gateway.RemoveEnvironmentValues(env, "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")
		// OpenClaw's local Browser client connects directly to this instance's
		// loopback Gateway. Give that interactive surface the same per-instance
		// secret that ClawManager already manages instead of trusting every
		// process in a pooled Runtime through allowLoopback.
		if instanceToken := strings.TrimSpace(req.Environment["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = gateway.SetEnvironmentValue(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		} else if instanceToken := strings.TrimSpace(req.Env["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = gateway.SetEnvironmentValue(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		}
	} else if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		if token, ok := gateway.RequestEnvValue(req, "CLAWMANAGER_INSTANCE_TOKEN"); ok {
			env = gateway.SetEnvironmentValue(env, "OPENCLAW_GATEWAY_TOKEN", token)
		}
	} else if cfg.GatewayToken != "" {
		env = gateway.SetEnvironmentValue(env, "OPENCLAW_GATEWAY_TOKEN", cfg.GatewayToken)
	}
	return env
}
