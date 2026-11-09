package configmanager

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVisionEnvironmentAndNormalization(t *testing.T) {
	t.Setenv("CLAWMANAGER_LLM_MODEL", `["auto","text"]`)
	t.Setenv("CLAWMANAGER_LLM_VISION", `{"auto":true,"text":false}`)
	overrides, _, err := readLLMOverridesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	normalizeLLMConfigContent(doc, overrides)
	models := doc["models"].(map[string]any)["providers"].(map[string]any)["auto"].(map[string]any)["models"].([]any)
	raw, _ := json.Marshal(models[0].(map[string]any)["input"])
	if !strings.Contains(string(raw), "image") {
		t.Fatal(string(raw))
	}
	raw, _ = json.Marshal(models[1].(map[string]any)["input"])
	if strings.Contains(string(raw), "image") {
		t.Fatal(string(raw))
	}
	t.Setenv("CLAWMANAGER_LLM_VISION", `{"auto":null}`)
	if _, _, err := readLLMOverridesFromEnv(); err == nil {
		t.Fatal("null accepted")
	}
}
