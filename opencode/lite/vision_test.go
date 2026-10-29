package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestVisionModelConfig(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		models := modelsMap([]string{"auto", "text-only"}, map[string]bool{"auto": enabled, "text-only": false})
		raw, _ := json.Marshal(models["auto"])
		if strings.Contains(string(raw), "image") != enabled {
			t.Fatal(string(raw))
		}
		raw, _ = json.Marshal(models["text-only"])
		if strings.Contains(string(raw), "image") {
			t.Fatal("text model advertised images")
		}
	}
	raw, _ := json.Marshal(modelsMap([]string{"legacy"}))
	if strings.Contains(string(raw), "capabilities") {
		t.Fatal("legacy config changed")
	}
}

func TestVisionSurvivesSharedConfigResolution(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "qualified"}[qualified], func(t *testing.T) {
			env := map[string]string{
				"CLAWMANAGER_LLM_BASE_URL": "https://gateway.example/v1",
				"CLAWMANAGER_LLM_API_KEY":  "test-key",
				"CLAWMANAGER_LLM_MODEL":    `["image-model","text-model"]`,
				"CLAWMANAGER_LLM_VISION":   `{"image-model":true,"text-model":false,"openai/image-model":false,"bare-model":true}`,
			}
			if qualified {
				env["CLAWMANAGER_LLM_PROVIDER_MODELS"] = `["openai/image-model","other/image-model","openai/text-model","bare-model"]`
			}
			for key, value := range env {
				t.Setenv(key, value)
			}
			t.Setenv("CLAWMANAGER_LLM_PROVIDER_MODELS", env["CLAWMANAGER_LLM_PROVIDER_MODELS"])
			gatewaySettings, err := resolveLLMSettings(gateway.Config{}, gateway.CreateGatewayRequest{Environment: env})
			if err != nil {
				t.Fatal(err)
			}
			desktopSettings, err := LoadLLMSettingsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			gatewayRaw, err := RenderConfig(gatewaySettings)
			if err != nil {
				t.Fatal(err)
			}
			desktopRaw, err := RenderConfig(desktopSettings)
			if err != nil {
				t.Fatal(err)
			}
			if string(gatewayRaw) != string(desktopRaw) {
				t.Fatal("desktop and gateway configs differ")
			}
			var doc map[string]any
			if err := json.Unmarshal(gatewayRaw, &doc); err != nil {
				t.Fatal(err)
			}
			check := func(provider, model string, image bool) {
				t.Helper()
				config := doc["provider"].(map[string]any)[provider].(map[string]any)["models"].(map[string]any)[model]
				raw, _ := json.Marshal(config)
				if strings.Contains(string(raw), `"image"`) != image {
					t.Fatalf("%s/%s: %s", provider, model, raw)
				}
			}
			if qualified {
				check("openai", "image-model", false)
				check("other", "image-model", true)
				check("openai", "text-model", false)
				check("auto", "bare-model", true)
			} else {
				check("clawmanager", "image-model", true)
				check("clawmanager", "text-model", false)
			}
		})
	}
}
