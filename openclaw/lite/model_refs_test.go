package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGatewayConfigMigratesInvalidModelRefsAcrossRestarts(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	const bad = "deepseek-v4.1-flash-public (openai)"
	ref := bad + "/" + bad
	original, _ := json.Marshal(map[string]any{
		"agents": map[string]any{"defaults": map[string]any{"modelPolicy": map[string]any{"allow": []string{"auto/auto", ref}, "deny": []string{ref}}}},
	})
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OpenClawVersion: "2026.10.2", LLMBaseURL: "http://gateway/api/v1/gateway/llm", LLMAPIKey: "token", LLMAPIKeySet: true, LLMModelsQualified: true, LLMModelIDs: []string{"auto/auto", ref}}
	req := CreateGatewayRequest{InstanceID: 63, UserID: 45, UID: os.Getuid(), GID: os.Getgid()}
	for i := 0; i < 2; i++ {
		if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		defaults := objectAt(t, objectAt(t, config, "agents"), "defaults")
		policy := objectAt(t, defaults, "modelPolicy")
		for _, key := range []string{"allow", "deny"} {
			for _, raw := range policy[key].([]any) {
				r := raw.(string)
				if strings.ContainsAny(r, " ()") {
					t.Fatalf("invalid %s ref survived restart: %s", key, r)
				}
			}
		}
		if !strings.Contains(string(data), bad) {
			t.Fatal("human-readable name lost")
		}
	}
	backup, err := os.ReadFile(path + ".model-ref-v1.bak")
	if err != nil || string(backup) != string(original) {
		t.Fatal("original config not preserved", err)
	}
}
