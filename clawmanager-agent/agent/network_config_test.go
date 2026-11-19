package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime"
)

func TestNetworkConfigUsesActualTargetSlotCapacity(t *testing.T) {
	for _, count := range []int{1, 4, 8, 16} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom")
			t.Setenv("RUNTIME_GATEWAY_COMMAND", "custom-serve")
			t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", strings.Repeat("c", 32))
			t.Setenv("RUNTIME_AGENT_ROLE", "slot")
			t.Setenv("RUNTIME_NETWORK_SECURITY_PROFILE", "isolated-network-v1")
			t.Setenv("RUNTIME_NETWORK_SLOT_COUNT", fmt.Sprint(count))
			t.Setenv("RUNTIME_SLOT_INDEX", fmt.Sprint(count-1))
			t.Setenv("POD_UID", "pod-test")
			profile := runtime.StaticProfile{RuntimeType: "custom", RuntimeDefaults: gateway.RuntimeDefaults{WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 23000, GatewayPortEnd: 23002, GatewayPortBlockSize: 1, GatewayCapacity: 1, GatewayAuthMode: "token"}}
			cfg, err := LoadConfigFromEnv(profile)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.NetworkSecurityProfile != "isolated-network-v1" || cfg.NetworkSlotCount != count || cfg.NetworkSlotIndex != count-1 {
				t.Fatal("lost target network capacity")
			}
			for _, value := range []string{"", "0", "257", "typo"} {
				t.Setenv("RUNTIME_NETWORK_SLOT_COUNT", value)
				if _, err = LoadConfigFromEnv(profile); err == nil {
					t.Fatal("accepted bad network slot count")
				}
			}
		})
	}
}
