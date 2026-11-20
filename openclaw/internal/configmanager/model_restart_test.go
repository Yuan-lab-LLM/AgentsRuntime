package configmanager

import (
	"encoding/json"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/managedmodelrefs"
	"reflect"
	"testing"
)

func TestRestartKeepsNormalizedModelParametersAndForeignPicker(t *testing.T) {
	const name = "managed (vendor)"
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "http://gateway/v1")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "owner")
	t.Setenv("CLAWMANAGER_LLM_MODEL", name)
	t.Setenv("CLAWMANAGER_LLM_VISION", "")
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.10.2")
	metadata := map[string]any{"id": managedmodelrefs.ModelID(name), "name": name, "contextWindow": float64(8192), "maxTokens": float64(1024), "reasoning": true}
	settings := map[string]any{"alias": "keep-alias", "params": map[string]any{"temperature": 0.3}}
	custom := map[string]any{"alias": "private"}
	ref := managedmodelrefs.Ref("auto", name)
	config := map[string]any{
		"models": map[string]any{"providers": map[string]any{"auto": map[string]any{"baseUrl": "http://gateway/v1", "apiKey": "owner", "models": []any{metadata}},
			"custom": map[string]any{"baseUrl": "http://gateway/v1", "apiKey": "other", "models": []any{map[string]any{"id": "private"}}}}},
		"agents": map[string]any{"defaults": map[string]any{"models": map[string]any{ref: settings, "custom/private": custom}}},
	}
	content, _ := json.Marshal(config)
	for i := 0; i < 3; i++ {
		output, _, err := normalizeConfigMap(content, appconfig.Config{})
		if err != nil {
			t.Fatal(err)
		}
		var merged map[string]any
		if err := json.Unmarshal(output, &merged); err != nil {
			t.Fatal(err)
		}
		provider := nestedMapForTest(t, merged, "models", "providers", "auto")
		if !reflect.DeepEqual(provider["models"].([]any)[0], metadata) {
			t.Fatal("model metadata changed", provider["models"])
		}
		entries := nestedMapForTest(t, merged, "agents", "defaults", "models")
		if !reflect.DeepEqual(entries[ref], settings) || !reflect.DeepEqual(entries["custom/private"], custom) {
			t.Fatal("picker entries lost", entries)
		}
		content = output
	}
}
