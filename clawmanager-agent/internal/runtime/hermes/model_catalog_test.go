package hermes

import (
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"gopkg.in/yaml.v3"
)

func TestHermesCatalogRefreshRemovesStaleModelsAndPreservesCustom(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: []string{"auto", "current"}}
	before := []byte(`model:
  temperature: 0.2
providers:
  clawmanager:
    base_url: http://gateway/v1/
    key_env: OPENAI_API_KEY
    models:
      auto: {}
      removed: {}
      current:
        supports_tools: true
  retired:
    base_url: http://gateway/v1
    key_env: OPENAI_API_KEY
    models:
      old-name: {}
  user:
    base_url: https://user.example/v1
    key_env: USER_KEY
    models:
      removed: {}
custom_setting: keep
`)
	for i := 0; i < 2; i++ {
		merged, err := mergeDesktopWebYAML(before, cfg, "http://backend/proxy", nil)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(merged, &doc); err != nil {
			t.Fatal(err)
		}
		providers := doc["providers"].(map[string]any)
		managed := providers["clawmanager"].(map[string]any)["models"].(map[string]any)
		if managed["removed"] != nil || providers["retired"] != nil || len(managed) != 2 {
			t.Fatal("stale catalog survived", string(merged))
		}
		if managed["current"].(map[string]any)["supports_tools"] != true || providers["user"] == nil || doc["custom_setting"] != "keep" {
			t.Fatal("user metadata lost")
		}
		before = merged
	}
}

func TestHermesCatalogCollisionDoesNotOverwriteUserProvider(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: []string{"auto"}}
	for _, before := range []string{
		"providers:\n  clawmanager:\n    base_url: https://user.example/v1\n    key_env: USER_KEY\n",
		"providers:\n  clawmanager:\n    base_url: http://gateway/v1\n    key_env: USER_KEY\n",
		"providers: []\n",
	} {
		if _, err := mergeDesktopWebYAML([]byte(before), cfg, "http://backend/proxy", nil); err == nil {
			t.Fatal("conflicting provider accepted", before)
		}
	}
	// Sharing an endpoint alone is not proof that a user provider is managed.
	before := []byte("providers:\n  user:\n    base_url: http://gateway/v1\n    key_env: USER_KEY\n")
	merged, err := mergeDesktopWebYAML(before, cfg, "http://backend/proxy", nil)
	if err != nil || !strings.Contains(string(merged), "USER_KEY") {
		t.Fatal("custom provider on same endpoint removed", err)
	}
}
