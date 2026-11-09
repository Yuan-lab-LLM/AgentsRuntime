package deepseek

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
)

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
