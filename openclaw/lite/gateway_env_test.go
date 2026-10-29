package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"path/filepath"
	"testing"
)

func TestOpenClawGatewayEnvIsolatesInstancePaths(t *testing.T) {
	base := []string{
		"HOME=/shared/home",
		"CLAWMANAGER_WORKSPACE_PATH=/shared/workspace",
		"CLAWMANAGER_AGENT_PERSISTENT_DIR=/shared/persistent",
	}
	tests := []struct {
		name          string
		userID        int
		instanceID    int
		workspacePath string
	}{
		{name: "gateway 123", userID: 45, instanceID: 123, workspacePath: filepath.Join("/workspaces", "openclaw", "user-45", "instance-123")},
		{name: "gateway 456", userID: 98, instanceID: 456, workspacePath: filepath.Join("/workspaces", "openclaw", "user-98", "instance-456")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := CreateGatewayRequest{
				AgentType:  "openclaw",
				UserID:     tc.userID,
				InstanceID: tc.instanceID,
				Environment: map[string]string{
					"HOME":                             "/request/home",
					"CLAWMANAGER_WORKSPACE_PATH":       "/request/workspace",
					"CLAWMANAGER_AGENT_PERSISTENT_DIR": "/request/persistent",
				},
			}
			env := OpenClawGatewayEnv(base, Config{RuntimeType: "openclaw", GatewayAuthMode: "trusted-proxy"}, req, tc.workspacePath, 20000+tc.instanceID%100)
			if got := gateway.EnvironmentValue(env, "CLAWMANAGER_WORKSPACE_PATH"); got != tc.workspacePath {
				t.Fatalf("CLAWMANAGER_WORKSPACE_PATH = %q, want %q", got, tc.workspacePath)
			}
			if got, want := gateway.EnvironmentValue(env, "HOME"), filepath.Join(tc.workspacePath, "home"); got != want {
				t.Fatalf("HOME = %q, want %q", got, want)
			}
			if got, want := gateway.EnvironmentValue(env, "CLAWMANAGER_AGENT_PERSISTENT_DIR"), filepath.Join(tc.workspacePath, "home", ".openclaw"); got != want {
				t.Fatalf("CLAWMANAGER_AGENT_PERSISTENT_DIR = %q, want %q", got, want)
			}
		})
	}
}

func TestOpenClawGatewayEnvUsesManagedInstancePasswordForLocalTools(t *testing.T) {
	req := CreateGatewayRequest{
		AgentType:  "openclaw",
		UserID:     45,
		InstanceID: 123,
		Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "instance-123-secret",
			"OPENCLAW_GATEWAY_PASSWORD":  "unmanaged-override",
			"OPENCLAW_GATEWAY_TOKEN":     "stale-token",
		},
	}
	env := OpenClawGatewayEnv(
		[]string{"OPENCLAW_GATEWAY_PASSWORD=base-password"},
		Config{RuntimeType: "openclaw", GatewayAuthMode: "trusted-proxy"},
		req,
		filepath.Join("/workspaces", "openclaw", "user-45", "instance-123"),
		20123,
	)
	if got := gateway.EnvironmentValue(env, "OPENCLAW_GATEWAY_PASSWORD"); got != "instance-123-secret" {
		t.Fatalf("OPENCLAW_GATEWAY_PASSWORD = %q, want managed instance token", got)
	}
	if got := gateway.EnvironmentValue(env, "OPENCLAW_GATEWAY_TOKEN"); got != "" {
		t.Fatalf("OPENCLAW_GATEWAY_TOKEN = %q, want unset in trusted-proxy mode", got)
	}
}

func TestOpenClawGatewayEnvDoesNotChangeTokenAuthContract(t *testing.T) {
	req := CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_INSTANCE_TOKEN": "instance-secret",
	}}
	env := OpenClawGatewayEnv(
		nil,
		Config{RuntimeType: "openclaw", GatewayAuthMode: "token", GatewayToken: "gateway-token"},
		req,
		filepath.Join("/workspaces", "openclaw", "user-1", "instance-2"),
		20002,
	)
	if got := gateway.EnvironmentValue(env, "OPENCLAW_GATEWAY_TOKEN"); got != "gateway-token" {
		t.Fatalf("OPENCLAW_GATEWAY_TOKEN = %q, want configured token", got)
	}
	if got := gateway.EnvironmentValue(env, "OPENCLAW_GATEWAY_PASSWORD"); got != "" {
		t.Fatalf("OPENCLAW_GATEWAY_PASSWORD = %q, want unset for token auth", got)
	}
}
func TestGatewayCannotOverrideManagedPolicyPath(t *testing.T) {
	req := CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_PROXY_POLICY_FILE": "/workspace/forged"}}
	for _, file := range []string{"", gateway.ManagedProxyPolicyFile} {
		env := OpenClawGatewayEnv(nil, Config{ProxyPolicyFile: file}, req, "/workspace", 20000)
		for _, item := range env {
			if item == "CLAWMANAGER_PROXY_POLICY_FILE=/workspace/forged" {
				t.Fatal("instance overrode policy")
			}
		}
	}
}
