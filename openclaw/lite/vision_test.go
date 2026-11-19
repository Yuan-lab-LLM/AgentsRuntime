package openclaw

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"strings"
	"testing"
)

func TestManagedVisionChangesInputOnly(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		doc := map[string]any{}
		mergeOpenClawLLMConfig(doc, gateway.Config{LLMModelIDs: []string{"auto", "text"}, LLMVision: map[string]bool{"auto": enabled, "text": false}})
		models := doc["models"].(map[string]any)["providers"].(map[string]any)["auto"].(map[string]any)["models"].([]any)
		raw, _ := json.Marshal(models[0].(map[string]any)["input"])
		if strings.Contains(string(raw), "image") != enabled {
			t.Fatal(string(raw))
		}
		raw, _ = json.Marshal(models[1].(map[string]any)["input"])
		if strings.Contains(string(raw), "image") {
			t.Fatal(string(raw))
		}
	}
}

func TestQualifiedVisionSurvivesRequestResolution(t *testing.T) {
	cfg, err := configWithRequestLLMEnv(gateway.Config{}, gateway.CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_LLM_PROVIDER_MODELS": `["openai/model","other/model"]`,
		"CLAWMANAGER_LLM_VISION":          `{"model":true,"openai/model":false}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	mergeOpenClawLLMConfig(doc, cfg)
	providers := doc["models"].(map[string]any)["providers"].(map[string]any)
	for provider, want := range map[string]bool{"openai": false, "other": true} {
		models := providers[provider].(map[string]any)["models"].([]any)
		raw, _ := json.Marshal(models[0].(map[string]any)["input"])
		if strings.Contains(string(raw), "image") != want {
			t.Fatalf("%s: %s", provider, raw)
		}
	}
}
