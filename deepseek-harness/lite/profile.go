package deepseek

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
	"os"
	"strings"
)

// Only the trusted slot lifecycle provides an instance-private outer /tmp.
// Legacy process pools must retain native DSH's isolated tmpfs behavior.
func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string, port int) []string {
	env := p.Profile.GatewayEnv(base, cfg, req, workspace, port)
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "CLAWMANAGER_DSH_PRIVATE_TMP=") && !strings.HasPrefix(entry, "CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=") {
			filtered = append(filtered, entry)
		}
	}
	value := "0"
	if cfg.IsolatedLifecycle {
		value = "1"
	}
	return append(filtered, "CLAWMANAGER_DSH_PRIVATE_TMP="+value, "CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION="+dshTeamCapability())
}

func dshTeamCapability() string {
	if os.Getenv("CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION") != "dsh-team-v1" {
		return ""
	}
	raw, err := os.ReadFile("/opt/clawmanager-dsh/adapter/deepseek-harness/team/capability.json")
	if err != nil {
		return ""
	}
	var v struct {
		Protocol string `json:"protocol"`
		Native   string `json:"native"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Protocol != "dsh-team-v1" || v.Native != "0.2.1-rc.3" {
		return ""
	}
	return v.Protocol
}

func (Profile) RegistrationMetadata(gateway.Config) gateway.RuntimeRegistrationMetadata {
	return gateway.RuntimeRegistrationMetadata{TeamPluginVersion: dshTeamCapability()}
}

type Profile struct {
	generic.Profile
}

func NewProfile(runtimeType string) Profile {
	return Profile{Profile: generic.NewProfile(runtimeType)}
}

func (Profile) DisplayName() string {
	return "DeepSeek Harness"
}

func (Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewHTTPGatewayHealthChecker(cfg)
}
