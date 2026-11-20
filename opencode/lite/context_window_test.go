package opencode

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
	"testing"
)

func TestManagedModelContextWrittenToOpenCode(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run(version, func(t *testing.T) {
			t.Setenv("OPENCODE_RUNTIME_MAJOR", version)
			t.Setenv("CLAWMANAGER_MODEL_GOVERNANCE_PROTOCOL", "")
			settings := llmconfig.Settings{BaseURL: "http://gateway/v1", ModelIDs: []string{"vendor/model"}, ModelsQualified: true, ContextWindows: map[string]int{"vendor/model": 64000}}
			settings.APIKey, settings.APIKeySet = "test-only-key", true
			raw, err := RenderConfig(settings)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			providerID := "vendor"
			if version == "2" {
				providerID = clawmanagerProviderID
			}
			providers, _ := doc["provider"].(map[string]any)
			provider, ok := providers[providerID].(map[string]any)
			if !ok {
				t.Fatalf("missing provider %s: %s", providerID, raw)
			}
			models, _ := provider["models"].(map[string]any)
			model, ok := models["model"].(map[string]any)
			if !ok {
				t.Fatalf("missing model: %s", raw)
			}
			limit, _ := model["limit"].(map[string]any)
			if limit["context"] != float64(64000) {
				t.Fatalf("context not written: %s", raw)
			}
			if version == "2" && limit["output"] != float64(32000) {
				t.Fatalf("missing v2 output bound: %s", raw)
			}
		})
	}
}
