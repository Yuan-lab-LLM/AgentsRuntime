package hermes

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const desktopReleasePath = "usr/local/share/hermes-lite/release.json"

func (p Profile) HealthCapabilities() *gateway.HealthCapabilities {
	return gateway.CloneHealthCapabilities(p.capabilities)
}

// WithVerifiedCapabilities is called only after deployment configuration loads.
// It does not inspect user workspaces, start processes, or access the network.
// Optional Desktop verification failure leaves ordinary agent health unchanged.
func (p Profile) WithVerifiedCapabilities(cfg gateway.Config) Profile {
	return p.withVerifiedCapabilities(cfg, string(filepath.Separator))
}

func (p Profile) withVerifiedCapabilities(cfg gateway.Config, managedRoot string) Profile {
	p.capabilities = nil
	if !p.desktopConfigurationVerified(cfg) {
		return p
	}
	root, err := os.OpenRoot(managedRoot)
	if err != nil {
		return p
	}
	defer root.Close()
	release, ok := readVerifiedDesktopRelease(root)
	if !ok {
		return p
	}
	if cfg.ProxyPolicyFile != "" && !desktopSHA256(release.Artifacts[HermesManagedProxyPolicyMarker]) {
		return p
	}
	p.capabilities = &gateway.HealthCapabilities{HermesDesktopWeb: &gateway.HermesDesktopWebCapability{
		ContractVersion: 2, Enabled: true, HermesRef: desktopWebHermesGitRef, HermesCommit: desktopWebHermesCommit,
		RPCProtocol: "hermes-jsonrpc-v1", BackendMode: "serve", AuthMode: "password-cookie",
		ArtifactsVerified: true, ReleaseAccepted: release.Accepted, PayloadSHA256: release.PayloadSHA256,
	}}
	return p
}

func (p Profile) desktopConfigurationVerified(cfg gateway.Config) bool {
	if p.runtimeType != "hermes" || cfg.RuntimeType != "hermes" || !p.desktopWeb || !desktopWebEnabled() {
		return false
	}
	if mode := strings.TrimSpace(os.Getenv("CLAWMANAGER_HERMES_BACKEND_MODE")); mode != "" && mode != "serve" {
		return false
	}
	// The fixed managed Lite launcher identifies the deployment profile. Team
	// membership belongs to individual gateway requests, not pod capabilities.
	if len(cfg.GatewayCommand) != 1 || (cfg.GatewayCommand[0] != "start-hermes-lite-backend" && cfg.GatewayCommand[0] != "/usr/local/bin/start-hermes-lite-backend" && cfg.GatewayCommand[0] != "start-hermes-lite-runtime" && cfg.GatewayCommand[0] != "/usr/local/bin/start-hermes-lite-runtime") {
		return false
	}
	if cfg.ControlToken == "" || cfg.ReportToken == "" || !filepath.IsAbs(cfg.WorkspaceRoot) || !filepath.IsAbs(cfg.AgentDataDir) || cfg.GatewayPortBlockSize != 1 || cfg.GatewayPortStart < 1 || cfg.GatewayPortEnd > 65535 || cfg.GatewayPortEnd < cfg.GatewayPortStart || cfg.Capacity < 1 {
		return false
	}
	if filepath.Clean(cfg.WorkspaceRoot) == string(filepath.Separator) || filepath.Clean(cfg.AgentDataDir) == string(filepath.Separator) {
		return false
	}
	if relative, err := filepath.Rel(cfg.WorkspaceRoot, cfg.AgentDataDir); err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return false
	}
	if !desktopInternalServiceURL(cfg.BackendURL) || !desktopInternalServiceURL(cfg.PublicOrigin) {
		return false
	}
	if _, _, err := desktopWebProxyConfig(cfg, gateway.CreateGatewayRequest{}); err != nil {
		return false
	}
	return true
}

func desktopInternalServiceURL(value string) bool {
	if !validDesktopWebURL(value, false) {
		return false
	}
	u, _ := url.Parse(value)
	host := u.Hostname()
	var service string
	if strings.HasSuffix(host, ".svc.cluster.local") {
		service = strings.TrimSuffix(host, ".svc.cluster.local")
	} else if strings.HasSuffix(host, ".svc") {
		service = strings.TrimSuffix(host, ".svc")
	} else {
		return false
	}
	labels := strings.Split(service, ".")
	if len(labels) != 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}
