package opencode

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

const (
	clawmanagerProviderID   = "clawmanager"
	clawmanagerProviderName = "ClawManager AI Gateway"
	defaultModelID          = "auto"
)

func WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	opencodeHome := filepath.Join(workspacePath, "home", ".opencode")
	if err := os.MkdirAll(opencodeHome, 0o750); err != nil {
		return fmt.Errorf("create opencode home: %w", err)
	}

	baseURL, apiKey, models, err := resolveLLMSettings(cfg, req)
	if err != nil {
		return err
	}

	configPath := filepath.Join(opencodeHome, "opencode.json")
	doc := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   clawmanagerProviderID + "/" + models[0],
		"provider": map[string]any{
			clawmanagerProviderID: map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": clawmanagerProviderName,
				"options": map[string]any{
					"baseURL": baseURL,
					"apiKey":  "{env:CLAWMANAGER_LLM_API_KEY}",
				},
				"models": modelsMap(models),
			},
		},
		"enabled_providers":  []string{clawmanagerProviderID},
		"disabled_providers": []string{"openai", "anthropic", "google", "amazon-bedrock", "azure", "groq", "mistral", "deepseek"},
	}
	_ = apiKey // referenced via env substitution in config; required to exist at write time

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal opencode config: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(configPath, raw, 0o640); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}
	if err := chownTree(opencodeHome, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown opencode home: %w", err)
	}
	return nil
}

func resolveLLMSettings(cfg gateway.Config, req gateway.CreateGatewayRequest) (baseURL, apiKey string, models []string, err error) {
	baseURL = strings.TrimRight(strings.TrimSpace(cfg.LLMBaseURL), "/")
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"); ok {
		baseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}
	if baseURL == "" {
		return "", "", nil, fmt.Errorf("missing OpenCode LLM base URL")
	}

	apiKey = strings.TrimSpace(cfg.LLMAPIKey)
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY", "CLAWMANAGER_INSTANCE_TOKEN"); ok {
		apiKey = strings.TrimSpace(value)
	}
	if apiKey == "" {
		return "", "", nil, fmt.Errorf("missing OpenCode LLM API key")
	}

	models = []string{defaultModelID}
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_MODEL", "OPENAI_MODEL"); ok {
		models = appendModels(models, value)
	}
	return baseURL, apiKey, models, nil
}

func appendModels(models []string, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return models
	}
	seen := map[string]bool{}
	for _, model := range models {
		seen[model] = true
	}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, id)
	}

	if strings.HasPrefix(raw, "[") {
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err == nil {
			for _, item := range list {
				add(item)
			}
			return models
		}
		var objects []map[string]any
		if err := json.Unmarshal([]byte(raw), &objects); err == nil {
			for _, item := range objects {
				if id, ok := item["id"].(string); ok {
					add(id)
				} else if id, ok := item["model"].(string); ok {
					add(id)
				}
			}
			return models
		}
	}
	add(raw)
	return models
}

func modelsMap(models []string) map[string]any {
	out := make(map[string]any, len(models))
	for _, id := range models {
		name := id
		if id == defaultModelID {
			name = "ClawManager Auto"
		}
		out[id] = map[string]any{"name": name}
	}
	return out
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return gateway.ChownWorkspace(path, uid, gid)
	})
}
