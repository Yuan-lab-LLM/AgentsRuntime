package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
	"testing"
)

func TestStartupPruneRemovesOwnedRefsButPreservesForeign(t *testing.T) {
	config := map[string]any{"models": map[string]any{"providers": map[string]any{"old": map[string]any{}, "custom": map[string]any{}}}, "agents": map[string]any{"defaults": map[string]any{
		"model":       map[string]any{"primary": "old/gone", "fallbacks": []any{"old/gone", "custom/own"}},
		"models":      map[string]any{"old/gone": map[string]any{}, "custom/own": map[string]any{}},
		"modelPolicy": map[string]any{"allow": []any{"old/gone", "custom/own"}},
	}}}
	pruneStartupModelCatalog(config, map[string]bool{"old": true, "clawmanager": true}, []llmconfig.ModelRef{{ProviderID: "clawmanager", ModelID: "cm-deploy-v1-new", Qualified: "clawmanager/cm-deploy-v1-new"}})
	providers := config["models"].(map[string]any)["providers"].(map[string]any)
	defaults := config["agents"].(map[string]any)["defaults"].(map[string]any)
	if providers["old"] != nil || providers["custom"] == nil || defaults["models"].(map[string]any)["old/gone"] != nil || defaults["models"].(map[string]any)["custom/own"] == nil {
		t.Fatal("unsafe pruning", config)
	}
	if defaults["model"].(map[string]any)["primary"] != nil {
		t.Fatal("stale primary retained")
	}
	allow := defaults["modelPolicy"].(map[string]any)["allow"].([]any)
	if len(allow) != 2 || allow[0] != "custom/own" || allow[1] != "clawmanager/cm-deploy-v1-new" {
		t.Fatal("incorrect allow list", allow)
	}
}
