package runtime

import (
	"fmt"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type RuntimeDefaults = gateway.RuntimeDefaults
type RuntimeProfile = gateway.RuntimeProfile

type StaticProfile struct {
	RuntimeType     string
	RuntimeName     string
	RuntimeDefaults RuntimeDefaults
}

func (p StaticProfile) Type() string {
	return p.RuntimeType
}

func (p StaticProfile) DisplayName() string {
	return p.RuntimeName
}

func (p StaticProfile) Defaults() RuntimeDefaults {
	return p.RuntimeDefaults
}

func (p StaticProfile) GatewayCommand(string) []string {
	return nil
}

func (p StaticProfile) GatewayEnv(base []string, _ gateway.Config, _ gateway.CreateGatewayRequest, _ string, _ int) []string {
	return base
}

func (p StaticProfile) PrepareWorkspace(_ gateway.Config, _ gateway.CreateGatewayRequest, workspacePath string) error {
	if workspacePath == "" {
		return fmt.Errorf("workspace path is empty")
	}
	return nil
}

func (p StaticProfile) WriteGatewayConfig(gateway.Config, gateway.CreateGatewayRequest, string, int) error {
	return nil
}

func (p StaticProfile) HealthChecker(gateway.Config) gateway.GatewayHealthChecker {
	return nil
}
