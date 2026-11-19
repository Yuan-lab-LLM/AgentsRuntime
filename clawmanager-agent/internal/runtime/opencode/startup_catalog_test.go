package opencode

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
	"testing"
)

func TestStartupCatalogOverridesStaleManagedContent(t *testing.T) {
	settings := llmconfig.Settings{BaseURL: "https://gateway.test/llm", APIKey: "test", APIKeySet: true, ModelIDs: []string{"auto", "cm-deploy-v1-new"}, ModelNames: map[string]string{"cm-deploy-v1-new": "same name"}, CatalogSynced: true}
	raw, err := RenderManagedConfig(settings, `{"provider":{"old":{"models":{"deleted":{}}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	provider := doc["provider"].(map[string]any)
	models := provider["clawmanager"].(map[string]any)["models"].(map[string]any)
	if provider["old"] != nil || models["deleted"] != nil || models["cm-deploy-v1-new"].(map[string]any)["name"] != "same name" {
		t.Fatal("stale content won", string(raw))
	}
	settings.ModelIDs = nil
	raw, err = RenderManagedConfig(settings, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["provider"].(map[string]any)["clawmanager"].(map[string]any)["models"].(map[string]any)["auto"] != nil {
		t.Fatal("empty directory invented auto")
	}
}
