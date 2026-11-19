package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
)

func imageEnvironment(profile gateway.RuntimeProfile) []string {
	result := []string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TERM", "SHELL"} {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
		}
	}
	if provider, ok := profile.(interface{ GatewayBaseEnvironment([]string) []string }); ok {
		return provider.GatewayBaseEnvironment(result)
	}
	return result
}
