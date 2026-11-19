package hermes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func desktopCapabilityConfig(t *testing.T) gateway.Config {
	t.Helper()
	t.Setenv(desktopWebFlag, "true")
	t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", "serve")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "")
	return gateway.Config{RuntimeType: "hermes", GatewayCommand: []string{"start-hermes-lite-backend"}, ControlToken: "test-private-control", ReportToken: "test-private-report", WorkspaceRoot: filepath.Join(t.TempDir(), "unused-workspaces"), AgentDataDir: filepath.Join(t.TempDir(), "unused-agent"), GatewayPortStart: 20000, GatewayPortEnd: 20099, GatewayPortBlockSize: 1, Capacity: 100, BackendURL: "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001", PublicOrigin: "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001", TrustedProxies: []string{"10.42.0.0/16"}, ImageRef: "registry.example/hermes@sha256:" + strings.Repeat("a", 64)}
}

func TestDesktopArtifactManagedPolicyMarkerIsExact(t *testing.T) {
	if !desktopArtifactPath(HermesManagedProxyPolicyMarker) {
		t.Fatal("verified managed policy marker rejected")
	}
	for _, file := range []string{
		HermesManagedProxyPolicyMarker + ".other",
		HermesManagedProxyPolicyMarker + "/child",
		"/usr/local/share/clawmanager/../clawmanager/hermes-proxy-policy-v1",
		"/usr/local/share/clawmanager/unverified",
	} {
		if desktopArtifactPath(file) {
			t.Fatalf("unverified marker path accepted: %q", file)
		}
	}
	for _, file := range desktopRequiredArtifacts {
		if !desktopArtifactPath(file) {
			t.Fatalf("legacy required artifact rejected: %q", file)
		}
	}
}

func TestDesktopCapabilityRequiresManagedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*gateway.Config)
	}{
		{"public backend", func(c *gateway.Config) { c.BackendURL = "https://public.example" }},
		{"public origin", func(c *gateway.Config) { c.PublicOrigin = "https://public.example" }},
		{"missing proxy", func(c *gateway.Config) { c.TrustedProxies = nil }},
		{"unbounded proxy", func(c *gateway.Config) { c.TrustedProxies = []string{"0.0.0.0/0"} }},
		{"unmanaged command", func(c *gateway.Config) { c.GatewayCommand = []string{"sh", "-c", "unverified"} }},
		{"Pro Desktop command", func(c *gateway.Config) { c.GatewayCommand = []string{"start-hermes-desktop"} }},
		{"legacy Team command", func(c *gateway.Config) { c.GatewayCommand = []string{"start-hermes-gateway"} }},
		{"non Hermes", func(c *gateway.Config) { c.RuntimeType = "openclaw" }},
		{"multiple ports", func(c *gateway.Config) { c.GatewayPortBlockSize = 5 }},
		{"missing control credential", func(c *gateway.Config) { c.ControlToken = "" }},
		{"relative workspace", func(c *gateway.Config) { c.WorkspaceRoot = "relative" }},
		{"agent state in user workspace", func(c *gateway.Config) { c.AgentDataDir = filepath.Join(c.WorkspaceRoot, "agent") }},
		{"invalid Service DNS", func(c *gateway.Config) { c.PublicOrigin = "http://gateway..svc:9001" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := desktopCapabilityConfig(t)
			p := NewProfile("hermes")
			if !p.desktopConfigurationVerified(cfg) {
				t.Fatal("fixture is invalid")
			}
			tc.edit(&cfg)
			if p.desktopConfigurationVerified(cfg) {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestDesktopCapabilityAcceptsTaggedImageReference(t *testing.T) {
	cfg := desktopCapabilityConfig(t)
	for _, image := range []string{"hermes:latest", "registry.example/hermes:v2026.9.12", "registry.local/hermes:offline"} {
		cfg.ImageRef = image
		if !NewProfile("hermes").desktopConfigurationVerified(cfg) {
			t.Errorf("tagged image reference %q suppressed Desktop capability", image)
		}
	}
}

func TestDesktopCapabilityProfileDoesNotUseInstanceTeamFlag(t *testing.T) {
	cfg := desktopCapabilityConfig(t)
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "true")
	if !NewProfile("hermes").desktopConfigurationVerified(cfg) {
		t.Fatal("instance Team flag changed managed Lite deployment identity")
	}
}

func TestDesktopCapabilityMissingReleaseDoesNotTouchUserState(t *testing.T) {
	cfg := desktopCapabilityConfig(t)
	p := NewProfile("hermes").withVerifiedCapabilities(cfg, t.TempDir())
	if p.HealthCapabilities() != nil {
		t.Fatal("missing release claimed compatibility")
	}
	if _, err := os.Stat(cfg.WorkspaceRoot); !os.IsNotExist(err) {
		t.Fatal("capability touched user workspace")
	}
	if _, err := os.Stat(cfg.AgentDataDir); !os.IsNotExist(err) {
		t.Fatal("capability touched runtime state")
	}
	t.Setenv(desktopWebFlag, "false")
	legacy := NewProfile("hermes")
	t.Setenv(desktopWebFlag, "true")
	if legacy.desktopConfigurationVerified(cfg) {
		t.Fatal("later environment change enabled legacy profile")
	}
	t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", "dashboard")
	if NewProfile("hermes").desktopConfigurationVerified(cfg) {
		t.Fatal("unverified backend declared capability")
	}
}
