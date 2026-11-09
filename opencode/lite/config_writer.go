package opencode

import (
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

const (
	clawmanagerProviderID   = "clawmanager"
	clawmanagerProviderName = "ClawManager AI Gateway"
	defaultModelID          = "auto"
)

var builtInProviderIDs = []string{
	"openai",
	"anthropic",
	"google",
	"amazon-bedrock",
	"azure",
	"groq",
	"mistral",
	"deepseek",
}

func WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	root, err := workspaceio.Ensure(workspacePath, 0750)
	if err != nil {
		return err
	}
	defer root.Close()
	opencodeHome := filepath.Join(workspacePath, "home", ".opencode")
	if err := root.MkdirAll(opencodeHome, 0o750); err != nil {
		return fmt.Errorf("create opencode home: %w", err)
	}

	settings, err := resolveLLMSettings(cfg, req)
	if err != nil {
		return err
	}

	configPath := filepath.Join(opencodeHome, "opencode.json")
	raw, err := RenderConfig(settings)
	if err != nil {
		return err
	}
	if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		return writeProtectedOpenCodeConfig(cfg, req, workspacePath, raw)
	}
	if err := root.WriteFile(configPath, raw, 0o640); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}
	if err := root.ChownTree(opencodeHome, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown opencode home: %w", err)
	}
	return nil
}

func resolveLLMSettings(cfg gateway.Config, req gateway.CreateGatewayRequest) (llmconfig.Settings, error) {
	return llmconfig.ResolveGateway(cfg, req, openCodeResolveOptions())
}

func LoadLLMSettingsFromEnv() (llmconfig.Settings, error) {
	return llmconfig.LoadFromEnv(openCodeResolveOptions())
}

func openCodeResolveOptions() llmconfig.ResolveOptions {
	return llmconfig.ResolveOptions{
		APIKeyEnvNames:    []string{"CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY", "CLAWMANAGER_INSTANCE_TOKEN"},
		LegacyModelPrefix: []string{defaultModelID},
	}
}

func normalizeLLMSettings(settings llmconfig.Settings) (llmconfig.Settings, error) {
	if settings.BaseURL == "" {
		return llmconfig.Settings{}, fmt.Errorf("missing OpenCode LLM base URL")
	}
	if !settings.APIKeySet || strings.TrimSpace(settings.APIKey) == "" {
		return llmconfig.Settings{}, fmt.Errorf("missing OpenCode LLM API key")
	}
	if len(settings.ModelIDs) == 0 {
		settings.ModelIDs = []string{defaultModelID}
		settings.ModelsQualified = false
	}
	return settings, nil
}

func RenderConfig(settings llmconfig.Settings) ([]byte, error) {
	settings, err := normalizeLLMSettings(settings)
	if err != nil {
		return nil, err
	}

	modelID := clawmanagerProviderID + "/" + settings.ModelIDs[0]
	apiKeyEnv := strings.TrimSpace(settings.APIKeySource)
	if apiKeyEnv == "" {
		apiKeyEnv = "CLAWMANAGER_LLM_API_KEY"
	}
	providers := legacyProviderConfig(settings.BaseURL, apiKeyEnv, settings.ModelIDs)
	enabledProviders := []string{clawmanagerProviderID}
	refs := settings.ModelRefs(clawmanagerProviderID)
	if settings.ModelsQualified {
		refs = settings.ModelRefs("auto")
		if len(refs) == 0 {
			return nil, fmt.Errorf("missing OpenCode LLM models")
		}
		modelID = refs[0].Qualified
		providers, enabledProviders = qualifiedProviderConfig(settings.BaseURL, apiKeyEnv, llmconfig.GroupModelRefs(refs))
	}
	for _, ref := range refs {
		enabled, managed := settings.Vision[ref.Qualified]
		if !managed {
			enabled, managed = settings.Vision[ref.ModelID]
		}
		if managed {
			provider := providers[ref.ProviderID].(map[string]any)
			model := provider["models"].(map[string]any)[ref.ModelID].(map[string]any)
			model["capabilities"] = map[string]any{"input": gateway.VisionInputModalities(nil, enabled), "output": []string{"text"}}
		}
	}
	doc := map[string]any{
		"$schema":            "https://opencode.ai/config.json",
		"model":              modelID,
		"provider":           providers,
		"enabled_providers":  enabledProviders,
		"disabled_providers": disabledProviders(enabledProviders),
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal opencode config: %w", err)
	}
	return append(raw, '\n'), nil
}

func legacyProviderConfig(baseURL, apiKeyEnv string, models []string) map[string]any {
	return map[string]any{
		clawmanagerProviderID: providerConfig(clawmanagerProviderName, baseURL, apiKeyEnv, models),
	}
}

func qualifiedProviderConfig(baseURL, apiKeyEnv string, groups []llmconfig.ProviderModels) (map[string]any, []string) {
	providers := make(map[string]any)
	providerOrder := make([]string, 0, len(groups))
	for _, group := range groups {
		providerOrder = append(providerOrder, group.ProviderID)
		providers[group.ProviderID] = providerConfig(group.ProviderID, baseURL, apiKeyEnv, group.ModelIDs)
	}
	return providers, providerOrder
}

func providerConfig(name, baseURL, apiKeyEnv string, models []string) map[string]any {
	return map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": name,
		"options": map[string]any{
			"baseURL": baseURL,
			"apiKey":  "{env:" + apiKeyEnv + "}",
		},
		"models": modelsMap(models),
	}
}

func disabledProviders(enabled []string) []string {
	enabledSet := make(map[string]struct{}, len(enabled))
	for _, providerID := range enabled {
		enabledSet[providerID] = struct{}{}
	}
	disabled := make([]string, 0, len(builtInProviderIDs))
	for _, providerID := range builtInProviderIDs {
		if _, exists := enabledSet[providerID]; !exists {
			disabled = append(disabled, providerID)
		}
	}
	return disabled
}

func modelsMap(models []string, visionMaps ...map[string]bool) map[string]any {
	out := make(map[string]any, len(models))
	for _, id := range models {
		name := id
		if id == defaultModelID {
			name = "ClawManager Auto"
		}
		out[id] = map[string]any{"name": name}
		if len(visionMaps) > 0 {
			if vision, managed := visionMaps[0][id]; managed {
				out[id].(map[string]any)["capabilities"] = map[string]any{"input": gateway.VisionInputModalities(nil, vision), "output": []string{"text"}}
			}
		}
	}
	return out
}
