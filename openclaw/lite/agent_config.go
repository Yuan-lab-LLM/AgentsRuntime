package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
)

func (p Profile) FinalizeConfig(cfg gateway.Config) (gateway.Config, error) {
	if marker, err := os.Stat(ManagedProxyPolicyMarker); p.Type() == "openclaw" && err == nil && marker.Mode().IsRegular() {
		cfg.ProxyPolicyFile = gateway.ManagedProxyPolicyFile
		cfg.TrustedProxies = nil
	}
	return cfg, nil
}

const ManagedProxyPolicyMarker = "/usr/local/share/clawmanager/openclaw-proxy-policy-v1"
