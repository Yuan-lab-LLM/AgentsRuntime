package opencode

import "github.com/iamlovingit/clawmanager-agent/gateway"

func (Profile) GatewayBaseEnvironment(base []string) []string {
	return gateway.AppendImageEnvironment(base, "NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_DISABLE_TELEMETRY", "PLAYWRIGHT_BROWSERS_PATH")
}
