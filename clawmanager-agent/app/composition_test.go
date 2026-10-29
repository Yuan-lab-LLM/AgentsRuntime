package app_test

import (
	"github.com/iamlovingit/clawmanager-agent/agent"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime"
	"testing"
)

func TestInjectedProfileControlsConfiguration(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "custom-serve")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report")
	profile := runtime.StaticProfile{RuntimeType: "custom", RuntimeName: "Custom", RuntimeDefaults: gateway.RuntimeDefaults{WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 23000, GatewayPortEnd: 23002, GatewayPortBlockSize: 1, GatewayCapacity: 3, GatewayAuthMode: "token"}}
	cfg, err := agent.LoadConfigFromEnv(profile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime != profile || cfg.Capacity != 3 || cfg.GatewayPortStart != 23000 {
		t.Fatalf("injected profile not preserved: %#v", cfg)
	}
}

func TestInjectedProfileRejectsRuntimeMismatch(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "other")
	_, err := agent.LoadConfigFromEnv(runtime.StaticProfile{RuntimeType: "custom"})
	if err == nil {
		t.Fatal("mismatched runtime accepted")
	}
}
