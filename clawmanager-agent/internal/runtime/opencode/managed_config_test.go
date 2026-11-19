package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
)

const managedDocument = `{"$schema":"https://opencode.ai/config.json","model":"clawmanager/auto","provider":{"clawmanager":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"{env:CLAWMANAGER_LLM_BASE_URL}","apiKey":"{env:CLAWMANAGER_LLM_API_KEY}"},"models":{"auto":{"name":"auto"},"m":{"name":"m","limit":{"context":8192}}}}}}`

func managedSettings() llmconfig.Settings {
	return llmconfig.Settings{BaseURL: "https://gateway.example/v1", APIKey: "never-write-secret", APIKeySet: true, APIKeySource: "CLAWMANAGER_LLM_API_KEY", ModelsQualified: true, ModelIDs: []string{"auto/auto", "old-provider/m"}, Vision: map[string]bool{"m": true}}
}

func TestManagedConfigWinsAndKeepsLimitsAndVision(t *testing.T) {
	raw, err := RenderManagedConfig(managedSettings(), managedDocument)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["provider"].(map[string]any)
	if len(providers) != 1 || doc["model"] != "clawmanager/auto" {
		t.Fatal("provider sources diverged", string(raw))
	}
	m := providers["clawmanager"].(map[string]any)["models"].(map[string]any)["m"].(map[string]any)
	if m["limit"].(map[string]any)["context"] != float64(8192) || len(m["capabilities"].(map[string]any)["input"].([]any)) != 2 {
		t.Fatal("capabilities lost", m)
	}
	if strings.Contains(string(raw), "never-write-secret") {
		t.Fatal("credential leaked")
	}
	for _, invalid := range []string{"{", "null", "{}", strings.ReplaceAll(managedDocument, "{env:CLAWMANAGER_LLM_API_KEY}", "literal-key"), strings.ReplaceAll(managedDocument, "{env:CLAWMANAGER_LLM_BASE_URL}", "https://external.invalid/v1")} {
		if _, err := RenderManagedConfig(managedSettings(), invalid); err == nil {
			t.Fatal("invalid source silently fell back")
		}
	}
	legacy, err := RenderManagedConfig(managedSettings(), "")
	if err != nil || !strings.Contains(string(legacy), "old-provider") {
		t.Fatal("legacy producer compatibility lost", err)
	}
}

func TestManagedMergePreservesCustomProviderAndSettings(t *testing.T) {
	config := filepath.Join(t.TempDir(), "opencode.json")
	old := `{"model":"custom/m","mcp":{"user-server":{"type":"local","command":["test"]}},"enabled_providers":["old-provider","custom"],"disabled_providers":["clawmanager","unused"],"provider":{"old-provider":{"options":{"baseURL":"https://gateway.example/v1","apiKey":"{env:CLAWMANAGER_LLM_API_KEY}"},"models":{"m":{}}},"custom":{"options":{"baseURL":"https://custom.invalid","apiKey":"custom-ref"},"models":{"m":{}}}}}`
	if err := os.WriteFile(config, []byte(old), 0o640); err != nil {
		t.Fatal(err)
	}
	raw, err := RenderManagedConfig(managedSettings(), managedDocument)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeManagedFile(config, raw, managedSettings())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["provider"].(map[string]any)
	if doc["model"] != "custom/m" || doc["mcp"] == nil || providers["custom"] == nil || providers["old-provider"] != nil || providers["clawmanager"] == nil {
		t.Fatal("custom config lost or stale provider retained", string(merged))
	}
	if len(doc["enabled_providers"].([]any)) != 2 || len(doc["disabled_providers"].([]any)) != 1 {
		t.Fatal("provider filters not migrated", doc)
	}
	if actual, _ := os.ReadFile(config); string(actual) != old {
		t.Fatal("merge modified original file")
	}
}

func TestLiteWritesSameManagedDocumentAndPreservesInvalidFile(t *testing.T) {
	workspace := t.TempDir()
	req := gateway.CreateGatewayRequest{UID: os.Getuid(), GID: os.Getgid(), Environment: map[string]string{
		"CLAWMANAGER_LLM_BASE_URL": "https://gateway.example/v1", "CLAWMANAGER_LLM_API_KEY": "never-write-secret",
		"CLAWMANAGER_LLM_PROVIDER_MODELS": `["auto/auto","old-provider/m"]`, "OPENCODE_CONFIG_CONTENT": managedDocument,
	}}
	if err := WriteGatewayConfig(gateway.Config{}, req, workspace); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(workspace, "home", ".opencode", "opencode.json")
	raw, err := os.ReadFile(config)
	if err != nil || strings.Contains(string(raw), "old-provider") || !strings.Contains(string(raw), "clawmanager/auto") {
		t.Fatal("Lite ignored authoritative config", err, string(raw))
	}
	if err := os.WriteFile(config, []byte("invalid original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteGatewayConfig(gateway.Config{}, req, workspace); err == nil {
		t.Fatal("invalid existing config was overwritten")
	}
	if actual, _ := os.ReadFile(config); string(actual) != "invalid original" {
		t.Fatal("original config lost")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(config), ".opencode-config-*"))
	if len(leftovers) != 0 {
		t.Fatal("temporary config files retained", leftovers)
	}
}

func TestManagedMergeRefusesForeignCanonicalProvider(t *testing.T) {
	raw, err := RenderManagedConfig(managedSettings(), managedDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{
		`{"provider":{"clawmanager":{"options":{"baseURL":"https://user.invalid/v1","apiKey":"user-key"}}}}`,
		`{"provider":{"clawmanager":{"options":{"baseURL":"https://gateway.example/v1","apiKey":"user-key"}}}}`,
		`{"provider":{"clawmanager":{}}}`,
		`{"provider":[]}`,
	} {
		path := filepath.Join(t.TempDir(), "opencode.json")
		if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := MergeManagedFile(path, raw, managedSettings()); err == nil {
			t.Fatal("foreign or invalid provider was overwritten")
		}
		if actual, err := os.ReadFile(path); err != nil || string(actual) != original {
			t.Fatal("original config changed", err)
		}
	}
}

func TestManagedMergeMigratesVerifiedLegacyCredentialSource(t *testing.T) {
	settings := managedSettings()
	settings.APIKeySource = "OPENAI_API_KEY"
	raw, err := RenderManagedConfig(settings, managedDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"{env:CLAWMANAGER_LLM_API_KEY}", "{env:OPENAI_API_KEY}", settings.APIKey} {
		path := filepath.Join(t.TempDir(), "opencode.json")
		original := `{"model":"old-provider/m","provider":{"old-provider":{"options":{"baseURL":"https://gateway.example/v1/","apiKey":"` + credential + `"}}}}`
		if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
			t.Fatal(err)
		}
		merged, err := MergeManagedFile(path, raw, settings)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(merged), "old-provider") || strings.Contains(string(merged), settings.APIKey) {
			t.Fatal("legacy provider or literal credential retained")
		}
	}
}

func TestLegacyProducerAlsoPreservesUserConfig(t *testing.T) {
	raw, err := RenderManagedConfig(managedSettings(), "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "opencode.json")
	original := `{"model":"custom/m","mcp":{"user-tool":{}},"provider":{"custom":{"models":{"m":{}}}},"enabled_providers":["custom"],"disabled_providers":["auto","unused"]}`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeManagedFile(path, raw, managedSettings())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["provider"].(map[string]any)
	if doc["model"] != "custom/m" || doc["mcp"] == nil || providers["custom"] == nil || providers["auto"] == nil || providers["old-provider"] == nil {
		t.Fatal("legacy rendering discarded user config")
	}
	if len(doc["enabled_providers"].([]any)) != 3 || len(doc["disabled_providers"].([]any)) != 1 {
		t.Fatal("legacy provider filters not merged")
	}
	if err := os.WriteFile(path, []byte(`{"provider":{"old-provider":{"models":{"m":{}}}}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeManagedFile(path, raw, managedSettings()); err == nil {
		t.Fatal("legacy producer overwrote a custom namespace")
	}
}
