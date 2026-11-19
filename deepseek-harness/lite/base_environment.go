package deepseek

import "github.com/iamlovingit/clawmanager-agent/gateway"

func (Profile) GatewayBaseEnvironment(base []string) []string {
	return gateway.AppendImageEnvironment(base, "NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "DSH_TELEMETRY_DISABLED", "PLAYWRIGHT_BROWSERS_PATH")
}
