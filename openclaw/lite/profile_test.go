package openclaw_test

import (
	"github.com/iamlovingit/clawmanager-agent/agent"
	"slices"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-openclaw-image/lite"
)

func TestOpenClawLiteGatewayAcceptsPodNetworkProxy(t *testing.T) {
	for _, auth := range []string{"trusted-proxy", "token"} {
		command := openclaw.NewProfile("openclaw").GatewayCommand(auth)
		for flag, want := range map[string]string{"--bind": "lan", "--auth": auth} {
			i := slices.Index(command, flag)
			if i < 0 || i+1 == len(command) || command[i+1] != want {
				t.Fatalf("%s must be %s for Pod-network access; command=%v", flag, want, command)
			}
		}
	}
	command := openclaw.NewProfile("openclaw-shell").GatewayCommand("trusted-proxy")
	if i := slices.Index(command, "--bind"); i < 0 || command[i+1] != "auto" {
		t.Fatalf("shell bind mode changed: %v", command)
	}
}

func TestProfileSupportsOpenClawRuntimeTypes(t *testing.T) {
	for _, runtimeType := range []string{"openclaw", "openclaw-shell"} {
		profile := openclaw.NewProfile(runtimeType)
		if profile.Type() != runtimeType {
			t.Fatalf("Type() = %q, want %q", profile.Type(), runtimeType)
		}
		if profile.Defaults().GatewayPortBlockSize != 3 {
			t.Fatalf("GatewayPortBlockSize = %d, want 3", profile.Defaults().GatewayPortBlockSize)
		}
	}
}

func TestOpenClawDefaultStartupBudgetSupportsLowCPU(t *testing.T) {
	// Under a shared CPU quota, native startup can exceed the old 90s budget.
	if got := openclaw.NewProfile("openclaw").Defaults().GatewayStartupTimeout; got != 5*time.Minute {
		t.Fatalf("OpenClaw startup budget=%s want 5m", got)
	}
	if got := openclaw.NewProfile("openclaw-shell").Defaults().GatewayStartupTimeout; got != 90*time.Second {
		t.Fatalf("Shell startup budget changed: %s", got)
	}
}

func TestOpenClawStartupBudgetPreservesEnvironmentOverride(t *testing.T) {
	for _, tc := range []struct {
		runtimeType, override string
		want                  time.Duration
	}{
		{"openclaw", "", 5 * time.Minute},
		{"openclaw", "150s", 150 * time.Second},
		{"openclaw", "45s", 45 * time.Second},
		{"openclaw-shell", "", 90 * time.Second},
	} {
		t.Run(tc.runtimeType+"/"+tc.override, func(t *testing.T) {
			t.Setenv("CLAWMANAGER_RUNTIME_TYPE", tc.runtimeType)
			t.Setenv("RUNTIME_AGENT_ROLE", "slot")
			t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "test-control")
			t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "test-report")
			t.Setenv("POD_NAMESPACE", "tenant")
			t.Setenv("POD_IP", "10.42.0.31")
			t.Setenv("RUNTIME_WORKSPACE_ROOT", t.TempDir())
			t.Setenv("RUNTIME_AGENT_DATA_DIR", t.TempDir())
			t.Setenv("RUNTIME_GATEWAY_STARTUP_HEALTH_TIMEOUT", tc.override)
			cfg, err := agent.LoadConfigFromEnv(openclaw.NewProfile(tc.runtimeType))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.GatewayStartupTimeout != tc.want {
				t.Fatalf("configured timeout=%s want %s", cfg.GatewayStartupTimeout, tc.want)
			}
		})
	}
}
