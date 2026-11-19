package opencode

import (
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestSlotPreservesDeliveredOpenCodeCapabilitiesWithoutRequestEscalation(t *testing.T) {
	p := NewProfile("opencode")
	for _, delivered := range []bool{true, false} {
		t.Run(map[bool]string{true: "governed", false: "ordinary-legacy"}[delivered], func(t *testing.T) {
			caps := map[string]string{"CLAWMANAGER_MODEL_GOVERNANCE_PROTOCOL": "1", "CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION": "opencode-team-v1", "OPENCODE_RUNTIME_MAJOR": "2"}
			for key, value := range caps {
				if !delivered {
					value = ""
				}
				t.Setenv(key, value)
			}
			base := p.GatewayBaseEnvironment([]string{"PATH=/usr/bin:/bin"})
			for key, value := range caps {
				if delivered && !strings.Contains(strings.Join(base, "\n"), key+"="+value) {
					t.Fatal("image capability lost before native helper", key)
				}
			}
			env := p.GatewayEnv(base, gateway.Config{}, gateway.CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_MODEL_GOVERNANCE_PROTOCOL": "attacker", "CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION": "opencode-team-v1", "OPENCODE_RUNTIME_MAJOR": "99"}}, t.TempDir(), 20000)
			for key, value := range caps {
				if !delivered {
					value = ""
				}
				found := false
				for _, entry := range env {
					if strings.HasPrefix(entry, key+"=") {
						found = true
						if entry != key+"="+value {
							t.Fatal("request overrode image capability", key)
						}
					}
				}
				if !found {
					t.Fatal("image capability missing", key)
				}
			}
		})
	}
}
