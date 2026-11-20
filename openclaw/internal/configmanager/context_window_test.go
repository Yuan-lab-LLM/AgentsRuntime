package configmanager

import "testing"

func TestProManagedContextWritesAndRefreshes(t *testing.T) {
	t.Setenv("CLAWMANAGER_LLM_MODEL", `["auto","model"]`)
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "http://gateway/v1")
	t.Setenv("CLAWMANAGER_LLM_CONTEXT_WINDOW", "32000")
	t.Setenv("CLAWMANAGER_LLM_CONTEXT_WINDOWS", `{"auto/auto":16000,"model":64000}`)
	overrides, _, err := readLLMOverridesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"custom_setting": "keep"}
	if err := normalizeLLMConfigContent(doc, overrides); err != nil {
		t.Fatal(err)
	}
	models := doc["models"].(map[string]any)["providers"].(map[string]any)["auto"].(map[string]any)["models"].([]any)
	if models[0].(map[string]any)["contextWindow"] != 16000 || models[1].(map[string]any)["contextWindow"] != 64000 {
		t.Fatal("context not written")
	}
	overrides.ContextWindows["model"] = 4096
	if err := normalizeLLMConfigContent(doc, overrides); err != nil {
		t.Fatal(err)
	}
	models = doc["models"].(map[string]any)["providers"].(map[string]any)["auto"].(map[string]any)["models"].([]any)
	if models[1].(map[string]any)["contextWindow"] != 4096 || doc["custom_setting"] != "keep" {
		t.Fatal("context refresh lost configuration")
	}
}
