package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestStartupCatalogMergePrunesOwnedButPreservesForeignProvider(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMAPIKey: "test", LLMAPIKeySet: true, LLMCatalogSynced: true, LLMModelIDs: []string{"auto", "cm-deploy-v1-new"}}
	base := `model:
  provider: mine
  default: my-model
providers:
  mine:
    base_url: https://mine.example/v1
    models:
      my-model: {}
  clawmanager:
    base_url: http://gateway/v1
    key_env: OPENAI_API_KEY
    models:
      removed: {}
custom_setting: keep
`
	raw, err := mergeHermesStartupYAML(base, buildManagedYAML(cfg), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["providers"].(map[string]any)
	if providers["mine"] == nil || doc["custom_setting"] != "keep" || doc["model"].(map[string]any)["provider"] != "mine" {
		t.Fatal("foreign settings changed", string(raw))
	}
	models := providers["clawmanager"].(map[string]any)["models"].(map[string]any)
	if models["removed"] != nil || models["cm-deploy-v1-new"] == nil {
		t.Fatal("catalog not reconciled", string(raw))
	}
}
