package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestManagedModelContextWrittenToOpenClaw(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: []string{"vendor/model"}, LLMModelsQualified: true, LLMContextWindows: map[string]int{"vendor/model": 4096}}
	doc := map[string]any{}
	if err := mergeOpenClawLLMConfig(doc, cfg); err != nil {
		t.Fatal(err)
	}
	model := doc["models"].(map[string]any)["providers"].(map[string]any)["vendor"].(map[string]any)["models"].([]any)[0].(map[string]any)
	if model["contextWindow"] != 4096 {
		t.Fatalf("wrong context: %v", model)
	}
	if limit, ok := model["maxTokens"].(int); ok && limit > 4096 {
		t.Fatal("output exceeds context")
	}
}
