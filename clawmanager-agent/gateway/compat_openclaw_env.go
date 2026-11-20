package gateway

import (
	"path/filepath"
	"strconv"
	"strings"
)

func OpenClawGatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = ApplyRequestEnvironment(env, req)
	env = unsetEnv(env, TaskReceiptRootEnv)
	if root := envValue(base, TaskReceiptRootEnv); root != "" {
		env = unsetEnv(env, "CLAWMANAGER_TASK_RECEIPT_DIR")
		if directory, err := ReceiptDirectoryPath(root, cfg.WorkspaceRoot, req); err == nil {
			env = setEnv(env, "CLAWMANAGER_TASK_RECEIPT_DIR", directory)
		}
	}
	// The lifecycle request owns the generation; never reuse an inherited or
	// stale serialized environment value after an instance restart/upgrade.
	env = unsetEnv(env, "CLAWMANAGER_GATEWAY_GENERATION")
	env = setEnv(env, "CLAWMANAGER_GATEWAY_GENERATION", strconv.Itoa(req.Generation))
	// Control-plane policy always overrides untrusted instance environment.
	env = unsetEnv(env, "CLAWMANAGER_PROXY_POLICY_FILE")
	env = unsetEnv(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE")
	if cfg.ProxyPolicyFile != "" {
		env = setEnv(env, "CLAWMANAGER_PROXY_POLICY_FILE", cfg.ProxyPolicyFile)
		env = setEnv(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE", cfg.Namespace)
	}
	env = ApplyLiteTeamConfigEnvironment(env, req, workspacePath)
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_AGENT_PERSISTENT_DIR", filepath.Join(workspacePath, "home", ".openclaw"))
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	env = setEnv(env, "HOME", filepath.Join(workspacePath, "home"))
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))
	env = setEnv(env, "OPENCLAW_HOST", "0.0.0.0")
	env = setEnv(env, "OPENCLAW_PORT", strconv.Itoa(port))
	env = setEnv(env, "OPENCLAW_GATEWAY_PORT", strconv.Itoa(port))
	if cfg.GatewayAuthMode == "trusted-proxy" {
		env = unsetEnv(env, "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")
		// OpenClaw's local Browser client connects directly to this instance's
		// loopback Gateway. Give that interactive surface the same per-instance
		// secret that ClawManager already manages instead of trusting every
		// process in a pooled Runtime through allowLoopback.
		if instanceToken := strings.TrimSpace(req.Environment["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = setEnv(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		} else if instanceToken := strings.TrimSpace(req.Env["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = setEnv(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		}
	} else if cfg.GatewayToken != "" {
		env = setEnv(env, "OPENCLAW_GATEWAY_TOKEN", cfg.GatewayToken)
	}
	return env
}
