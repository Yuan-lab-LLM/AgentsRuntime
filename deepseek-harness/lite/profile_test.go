package deepseek

import (
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestPrivateTemporaryMountRequiresTrustedSlotLifecycle(t *testing.T) {
	profile := NewProfile("deepseek-harness")
	for _, isolated := range []bool{false, true} {
		cfg := gateway.Config{IsolatedLifecycle: isolated}
		req := gateway.CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_DSH_PRIVATE_TMP": "1"}}
		env := profile.GatewayEnv([]string{"CLAWMANAGER_DSH_PRIVATE_TMP=1"}, cfg, req, "/workspaces/deepseek-harness/user-1/instance-4", 20000)
		count, wanted := 0, "CLAWMANAGER_DSH_PRIVATE_TMP=0"
		if isolated {
			wanted = "CLAWMANAGER_DSH_PRIVATE_TMP=1"
		}
		for _, entry := range env {
			if strings.HasPrefix(entry, "CLAWMANAGER_DSH_PRIVATE_TMP=") {
				count++
				if entry != wanted {
					t.Fatalf("untrusted temporary mount flag: %s", entry)
				}
			}
		}
		if count != 1 {
			t.Fatalf("mount flags=%d", count)
		}
	}
}

func TestProfileUsesManagedHTTPReadiness(t *testing.T) {
	profile := NewProfile("deepseek-harness")
	if profile.Type() != "deepseek-harness" {
		t.Fatalf("Type() = %q", profile.Type())
	}
	if profile.DisplayName() != "DeepSeek Harness" {
		t.Fatalf("DisplayName() = %q", profile.DisplayName())
	}
	if profile.Defaults().GatewayPortBlockSize != 1 {
		t.Fatalf("GatewayPortBlockSize = %d, want 1", profile.Defaults().GatewayPortBlockSize)
	}
	if profile.HealthChecker(gateway.Config{}) == nil {
		t.Fatal("HealthChecker() = nil")
	}
}

func TestTeamCapabilityCannotBeGrantedByRequest(t *testing.T) {
	t.Setenv("CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION", "")
	p := NewProfile("deepseek-harness")
	env := p.GatewayEnv([]string{"CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=dsh-team-v1"}, gateway.Config{}, gateway.CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION": "dsh-team-v1"}}, "/workspaces/deepseek-harness/user-1/instance-4", 20000)
	for _, e := range env {
		if e == "CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=dsh-team-v1" {
			t.Fatal("request granted image capability")
		}
	}
	if p.RegistrationMetadata(gateway.Config{}).TeamPluginVersion != "" {
		t.Fatal("non-Team image advertised adapter")
	}
}
