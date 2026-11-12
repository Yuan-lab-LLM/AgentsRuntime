package openclaw

import (
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

func TestIsolatedModelRelayRequestPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, profile, version, base string
		want                         bool
	}{
		{"native 10.2", "isolated-network-v1", "2026.10.2", managedconfig.ModelURL, true},
		{"legacy core", "isolated-network-v1", "2026.7.1", managedconfig.ModelURL, false},
		{"ordinary provider", "", "2026.10.2", "https://vendor.example/v1", false},
		{"outside native relay", "isolated-network-v1", "2026.10.2", "http://other.internal/v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providers := map[string]any{"auto": map[string]any{"request": map[string]any{"timeoutMs": 12345}}}
			p := mergeOpenClawProviderConnection(providers, "auto", gateway.Config{NetworkSecurityProfile: tc.profile, OpenClawVersion: tc.version, LLMBaseURL: tc.base})
			r, _ := p["request"].(map[string]any)
			if (r["allowPrivateNetwork"] == true) != tc.want || r["timeoutMs"] != 12345 {
				t.Fatalf("unexpected scoped policy: %#v", r)
			}
		})
	}
}

func TestMigratedModelRelayPolicyRetainsExternalProviders(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		managed := map[string]any{"baseUrl": "http://backend.ns.svc/api/v1/gateway/llm", "request": map[string]any{"allowPrivateNetwork": false, "timeoutMs": 12345}}
		personal := map[string]any{"baseUrl": "https://vendor.example/v1", "request": map[string]any{"allowPrivateNetwork": false}}
		if err := migrateOpenClawModelProviders(map[string]any{"auto": managed, "personal": personal}, enabled); err != nil {
			t.Fatal(err)
		}
		r := managed["request"].(map[string]any)
		if r["allowPrivateNetwork"] != enabled || r["timeoutMs"] != 12345 || personal["request"].(map[string]any)["allowPrivateNetwork"] != false {
			t.Fatalf("unexpected policy: %#v / %#v", managed, personal)
		}
	}
	if err := migrateOpenClawModelProviders(map[string]any{"auto": map[string]any{"request": "malformed"}}, true); err == nil {
		t.Fatal("accepted malformed managed policy")
	}
}
