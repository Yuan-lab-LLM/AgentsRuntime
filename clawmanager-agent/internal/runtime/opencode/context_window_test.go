package opencode

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
	"testing"
)

func TestManagedModelContextWrittenToOpenCode(t *testing.T) {
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
	model := doc["provider"].(map[string]any)["vendor"].(map[string]any)["models"].(map[string]any)["model"].(map[string]any)
	if model["limit"].(map[string]any)["context"] != float64(64000) {
		t.Fatal("context not written")
	}
}
