package hermes

import (
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/url"
	"os"
	"strings"
)

func (p Profile) FinalizeConfig(cfg gateway.Config) (gateway.Config, error) {
	if p.desktopWeb {
		cfg.PublicOrigin = strings.TrimSpace(os.Getenv("CLAWMANAGER_CONTROL_UI_ORIGIN"))
		cfg.AllowedOrigins = nil
		cfg.TrustedProxies = nil
		if marker, err := os.Stat(HermesManagedProxyPolicyMarker); err == nil && marker.Mode().IsRegular() {
			cfg.ProxyPolicyFile = gateway.ManagedProxyPolicyFile
		}
		for _, cidr := range strings.Split(os.Getenv("CLAWMANAGER_TRUSTED_PROXY_CIDRS"), ",") {
			if cidr = strings.TrimSpace(cidr); cidr != "" {
				cfg.TrustedProxies = append(cfg.TrustedProxies, cidr)
			}
		}
		backend, err := url.Parse(cfg.BackendURL)
		if err != nil || backend.User != nil || backend.RawQuery != "" || backend.Fragment != "" || !(strings.HasSuffix(backend.Hostname(), ".svc.cluster.local") || strings.HasSuffix(backend.Hostname(), ".svc")) {
			return gateway.Config{}, errors.New("Hermes Lite requires CLAWMANAGER_BACKEND_URL to use Kubernetes Service DNS")
		}
	}
	cfg.Runtime = p.WithVerifiedCapabilities(cfg)
	return cfg, nil
}

const HermesManagedProxyPolicyMarker = "/usr/local/share/clawmanager/hermes-proxy-policy-v1"
