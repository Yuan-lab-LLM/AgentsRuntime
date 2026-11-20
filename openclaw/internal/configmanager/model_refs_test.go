package configmanager

import (
	"encoding/json"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"strings"
	"testing"
)

func TestNormalizeConfigMapRepairsManagedModelPolicy(t *testing.T) {
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "http://gateway/api/v1/gateway/llm")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "token")
	t.Setenv("CLAWMANAGER_LLM_MODEL", `["glm-5.3-public (openai)"]`)
	t.Setenv("CLAWMANAGER_LLM_VISION", "")
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.10.2")
	input := []byte(`{"agents":{"defaults":{"modelPolicy":{"allow":["auto/glm-5.3-public (openai)"]}}}}`)
	for i := 0; i < 2; i++ {
		output, _, err := normalizeConfigMap(input, appconfig.Config{})
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(output, &config); err != nil {
			t.Fatal(err)
		}
		defaults := config["agents"].(map[string]any)["defaults"].(map[string]any)
		policy := defaults["modelPolicy"].(map[string]any)
		for _, raw := range policy["allow"].([]any) {
			if strings.ContainsAny(raw.(string), " ()") {
				t.Fatalf("invalid ref: %v", raw)
			}
		}
		input = output
	}
}
