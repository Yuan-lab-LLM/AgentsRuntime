package openclaw

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/internal/managedmodelrefs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRestartRetainsNormalizedModelMetadataAndCustomEntries(t *testing.T) {
	const name = "managed (vendor)"
	const base = "http://gateway/v1"
	ref := managedmodelrefs.Ref(name, name)
	metadata := map[string]any{"id": managedmodelrefs.ModelID(name), "name": name, "contextWindow": float64(8192), "maxTokens": float64(1024), "compat": map[string]any{"custom": true}}
	settings := map[string]any{"alias": "keep-alias", "params": map[string]any{"temperature": 0.3}}
	custom := map[string]any{"alias": "private-custom"}
	config := map[string]any{
		"models": map[string]any{"providers": map[string]any{managedmodelrefs.ProviderID(name): map[string]any{"baseUrl": base, "apiKey": "owner", "models": []any{metadata}},
			"custom": map[string]any{"baseUrl": base, "apiKey": "other", "models": []any{map[string]any{"id": "private"}}}}},
		"agents": map[string]any{"defaults": map[string]any{"models": map[string]any{ref: settings, "custom/private": custom}}},
	}
	workspace := t.TempDir()
	path := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OpenClawVersion: "2026.10.2", LLMBaseURL: base, LLMAPIKey: "owner", LLMAPIKeySet: true, LLMModelsQualified: true, LLMModelIDs: []string{name + "/" + name}}
	req := CreateGatewayRequest{InstanceID: 63, UserID: 45, UID: os.Getuid(), GID: os.Getgid()}
	for i := 0; i < 3; i++ {
		if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
			t.Fatal(err)
		}
		merged := readOpenClawConfigForTest(t, path)
		provider := objectAt(t, objectAt(t, objectAt(t, merged, "models"), "providers"), managedmodelrefs.ProviderID(name))
		got := provider["models"].([]any)[0]
		if !reflect.DeepEqual(got, metadata) {
			t.Fatalf("restart %d model parameters changed: %#v", i, got)
		}
		entries := objectAt(t, objectAt(t, objectAt(t, merged, "agents"), "defaults"), "models")
		if !reflect.DeepEqual(entries[ref], settings) || !reflect.DeepEqual(entries["custom/private"], custom) {
			t.Fatal("picker metadata lost", entries)
		}
	}
}

func TestProviderCollisionPreservesConfigFile(t *testing.T) {
	const name = "managed (vendor)"
	for _, normalized := range []bool{false, true} {
		provider := name
		if normalized {
			provider = managedmodelrefs.ProviderID(name)
		}
		config := map[string]any{"models": map[string]any{"providers": map[string]any{provider: map[string]any{"baseUrl": "http://gateway/v1", "apiKey": "foreign", "models": []any{map[string]any{"id": "private"}}}}}}
		original, _ := json.Marshal(config)
		workspace := t.TempDir()
		path := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		cfg := Config{LLMBaseURL: "http://gateway/v1", LLMAPIKey: "owner", LLMAPIKeySet: true, LLMModelsQualified: true, LLMModelIDs: []string{name + "/" + name}}
		req := CreateGatewayRequest{InstanceID: 63, UserID: 45, UID: os.Getuid(), GID: os.Getgid()}
		if err := WriteGatewayConfig(cfg, req, workspace, 20003); err == nil {
			t.Fatal("foreign provider overwritten")
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(original) {
			t.Fatal("failed merge changed original file", err)
		}
	}
}
