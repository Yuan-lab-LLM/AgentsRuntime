package agent

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime"
	"strings"
	"testing"
)

func TestNFSRootlessAgentConfigRejectsMissingIsolation(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "custom-serve")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", strings.Repeat("c", 32))
	t.Setenv("RUNTIME_AGENT_ROLE", "slot")
	t.Setenv("RUNTIME_NETWORK_SECURITY_PROFILE", "isolated-network-v1")
	t.Setenv("RUNTIME_NETWORK_SLOT_COUNT", "2")
	t.Setenv("RUNTIME_SLOT_INDEX", "0")
	t.Setenv("POD_UID", "pod-test")
	t.Setenv("RUNTIME_SANDBOX_PROFILE", "nfs-rootless-v1")
	p := runtime.StaticProfile{RuntimeType: "custom", RuntimeDefaults: gateway.RuntimeDefaults{WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 23000, GatewayPortEnd: 23002, GatewayPortBlockSize: 1, GatewayCapacity: 1, GatewayAuthMode: "token"}}
	cfg, err := LoadConfigFromEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxProfile != "nfs-rootless-v1" {
		t.Fatal("lost NFS sandbox profile")
	}
	t.Setenv("RUNTIME_NETWORK_SECURITY_PROFILE", "")
	if _, err = LoadConfigFromEnv(p); err == nil {
		t.Fatal("accepted NFS profile without network isolation")
	}
	t.Setenv("RUNTIME_SANDBOX_PROFILE", "unknown")
	if _, err = LoadConfigFromEnv(p); err == nil {
		t.Fatal("accepted unknown sandbox profile")
	}
}

func TestNFSRootlessPoolRequiresPinnedIdentityAndStrongControlToken(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "custom-serve")
	t.Setenv("RUNTIME_AGENT_ROLE", "pool")
	t.Setenv("RUNTIME_AGENT_TLS_CERT_FILE", "/tls/cert")
	t.Setenv("RUNTIME_AGENT_TLS_KEY_FILE", "/tls/key")
	t.Setenv("RUNTIME_SANDBOX_PROFILE", "nfs-rootless-v1")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", strings.Repeat("r", 32))
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", strings.Repeat("c", 32))
	t.Setenv("POD_UID", "lab-pod")
	p := runtime.StaticProfile{RuntimeType: "custom", RuntimeDefaults: gateway.RuntimeDefaults{WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 23000, GatewayPortEnd: 23002, GatewayPortBlockSize: 1, GatewayCapacity: 1, GatewayAuthMode: "token"}}
	if _, err := LoadConfigFromEnv(p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "weak")
	if _, err := LoadConfigFromEnv(p); err == nil {
		t.Fatal("NFS pool accepted weak control token")
	}
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", strings.Repeat("c", 32))
	t.Setenv("POD_UID", "")
	if _, err := LoadConfigFromEnv(p); err == nil {
		t.Fatal("NFS pool accepted unpinned Pod identity")
	}
}
