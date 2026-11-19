package openclaw

import "github.com/iamlovingit/clawmanager-agent/gateway"

func (Profile) GatewayBaseEnvironment(base []string) []string {
	return gateway.AppendImageEnvironment(base, "NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "PLAYWRIGHT_BROWSERS_PATH", "PUPPETEER_EXECUTABLE_PATH", "CHROME_PATH", "CHROMIUM_PATH")
}
