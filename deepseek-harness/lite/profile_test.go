package deepseek

import (
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestProfileUsesManagedHTTPReadiness(t *testing.T) {
	profile := NewProfile("deepseek-harness")
	if profile.Type() != "deepseek-harness" {
		t.Fatalf("Type() = %q", profile.Type())
	}
	if profile.DisplayName() != "DeepSeek Harness" {
		t.Fatalf("DisplayName() = %q", profile.DisplayName())
	}
	if profile.Defaults().GatewayPortBlockSize != 1 {
		t.Fatalf("GatewayPortBlockSize = %d, want 1", profile.Defaults().GatewayPortBlockSize)
	}
	if profile.HealthChecker(gateway.Config{}) == nil {
		t.Fatal("HealthChecker() = nil")
	}
}
