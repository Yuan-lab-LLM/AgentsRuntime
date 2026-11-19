package openclaw

import (
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

func TestManagedModelBrokerPrivateNetworkPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, version, profile, base string
		allow                        bool
	}{
		{"protected broker", "2026.10.2", "isolated-network-v1", managedconfig.ModelURL, true},
		{"legacy native", "2026.8.1", "isolated-network-v1", managedconfig.ModelURL, false},
		{"unknown native", "", "isolated-network-v1", managedconfig.ModelURL, false},
		{"shared network", "2026.10.2", "", managedconfig.ModelURL, false},
		{"other provider", "2026.10.2", "isolated-network-v1", "http://private.example/v1", false},
		{"broker lookalike", "2026.10.2", "isolated-network-v1", managedconfig.ModelURL + ".evil", false},
		{"broker query", "2026.10.2", "isolated-network-v1", managedconfig.ModelURL + "?target=other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			custom := map[string]any{"baseUrl": "https://custom.example/v1"}
			providers := map[string]any{
				"auto":   map[string]any{"request": map[string]any{"timeoutMs": 1234}},
				"custom": custom,
			}
			doc := map[string]any{"models": map[string]any{"providers": providers}}
			mergeOpenClawLLMConfig(doc, gateway.Config{OpenClawVersion: tc.version, NetworkSecurityProfile: tc.profile, LLMBaseURL: tc.base, LLMModelIDs: []string{"auto/auto", "icompify/flash"}, LLMModelsQualified: true})
			request := providers["auto"].(map[string]any)["request"].(map[string]any)
			if got := request["allowPrivateNetwork"] == true; got != tc.allow {
				t.Fatalf("allowPrivateNetwork=%v, want %v", got, tc.allow)
			}
			if request["timeoutMs"] != 1234 {
				t.Fatal("lost existing request setting")
			}
			otherRequest, _ := providers["icompify"].(map[string]any)["request"].(map[string]any)
			if got := otherRequest["allowPrivateNetwork"] == true; got != tc.allow {
				t.Fatalf("qualified provider allowPrivateNetwork=%v, want %v", got, tc.allow)
			}
			if _, ok := custom["request"]; ok {
				t.Fatal("changed unrelated provider")
			}
		})
	}
}
