package runtime_test

import (
	"testing"
	"time"

	runtime "github.com/iamlovingit/clawmanager-agent/runtime"
)

func TestRegistryReturnsRegisteredProfile(t *testing.T) {
	registry := runtime.NewRegistry()
	profile := runtime.StaticProfile{RuntimeType: "demo", RuntimeName: "Demo"}
	if err := registry.Register(profile); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, ok := registry.Get("demo")
	if !ok || got.Type() != "demo" {
		t.Fatalf("Get(demo) = %#v, %v", got, ok)
	}
}

func TestRegistryRejectsDuplicateProfile(t *testing.T) {
	registry := runtime.NewRegistry()
	profile := runtime.StaticProfile{RuntimeType: "demo", RuntimeName: "Demo"}
	if err := registry.Register(profile); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if err := registry.Register(profile); err == nil {
		t.Fatal("Register duplicate error = nil, want error")
	}
}

func TestStaticProfileReturnsDefaults(t *testing.T) {
	defaults := runtime.RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/clawmanager-agent",
		GatewayPortStart:      20000,
		GatewayPortEnd:        20099,
		GatewayPortBlockSize:  3,
		GatewayCapacity:       100,
		GatewayAuthMode:       "trusted-proxy",
		GatewayStartupTimeout: 90 * time.Second,
	}
	profile := runtime.StaticProfile{
		RuntimeType:     "demo",
		RuntimeName:     "Demo",
		RuntimeDefaults: defaults,
	}

	if profile.DisplayName() != "Demo" {
		t.Fatalf("DisplayName() = %q, want Demo", profile.DisplayName())
	}
	if got := profile.Defaults(); got != defaults {
		t.Fatalf("Defaults() = %#v, want %#v", got, defaults)
	}
}
