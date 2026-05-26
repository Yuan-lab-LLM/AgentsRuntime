package configmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const autoProviderName = "auto"

// NormalizeActiveConfig reads the openclaw config at cfg.OpenClawConfigPath,
// applies both the LLM environment overrides and the channel/plugins
// reconciliation, and writes back only if the content actually changed.
func (m *Manager) NormalizeActiveConfig() error {
	normalized, changed, err := normalizeConfigFile(m.cfg.OpenClawConfigPath, m.cfg)
	if err != nil {
		return err
	}
	if changed {
		if err := os.WriteFile(m.cfg.OpenClawConfigPath, normalized, 0o600); err != nil {
			return err
		}
	}
	return normalizePluginInstallRegistry(m.cfg)
}

func normalizeConfigFile(path string, cfg appconfig.Config) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read openclaw config: %w", err)
	}

	normalized, changed, err := normalizeConfigMap(content, cfg)
	if err != nil {
		return nil, false, err
	}
	return normalized, changed, nil
}

// normalizeConfigMap mutates the in-memory cfg JSON with both LLM
// overrides (when any are set) and channel/plugins reconciliation (always),
// then re-encodes the result. The returned bool reports whether the
// re-encoded bytes differ from the input.
func normalizeConfigMap(content []byte, cfg appconfig.Config) ([]byte, bool, error) {
	parsed, err := parseConfigJSON(content)
	if err != nil {
		return nil, false, err
	}

	llm, hasLLMOverrides, err := readLLMOverridesFromEnv()
	if err != nil {
		return nil, false, err
	}
	if hasLLMOverrides {
		normalizeLLMConfigContent(parsed, llm)
	}
	normalizeProviderAuthContracts(parsed)

	channelOpts := readChannelOverridesFromEnv(cfg)
	if err := applyChannelOverrides(parsed, channelOpts); err != nil {
		return nil, false, err
	}

	normalized, err := rewriteConfig(parsed)
	if err != nil {
		return nil, false, err
	}
	return normalized, !bytes.Equal(content, normalized), nil
}

func normalizePluginInstallRegistry(cfg appconfig.Config) error {
	registryPath := filepath.Join(filepath.Dir(cfg.OpenClawConfigPath), "plugins", "installs.json")
	content, err := os.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read openclaw plugin registry: %w", err)
	}

	normalized, changed, err := normalizePluginInstallRegistryContent(content, cfg)
	if err != nil {
		return fmt.Errorf("normalize openclaw plugin registry: %w", err)
	}
	if !changed {
		return nil
	}
	return os.WriteFile(registryPath, normalized, 0o600)
}

func normalizePluginInstallRegistryContent(content []byte, cfg appconfig.Config) ([]byte, bool, error) {
	parsed, err := parseConfigJSON(content)
	if err != nil {
		return nil, false, err
	}

	if cfg.InstalledPluginPathPrefix != "" && cfg.OpenClawExtensionsDir != "" {
		rewritePluginPathStrings(parsed, cfg.InstalledPluginPathPrefix, cfg.OpenClawExtensionsDir)
	}
	if cfg.OpenClawDefaultsDir != "" && cfg.OpenClawConfigPath != "" {
		rewritePluginPathStrings(parsed, cfg.OpenClawDefaultsDir, filepath.Dir(cfg.OpenClawConfigPath))
	}

	normalized, err := rewriteConfig(parsed)
	if err != nil {
		return nil, false, err
	}
	return normalized, !bytes.Equal(content, normalized), nil
}

func rewritePathPrefix(value, prefix, replacement string) (string, bool) {
	if prefix == "" || replacement == "" {
		return value, false
	}
	normalizedValue := pathClean(value)
	normalizedPrefix := pathClean(prefix)
	if normalizedValue == normalizedPrefix {
		return pathClean(replacement), true
	}
	prefixWithSlash := strings.TrimSuffix(normalizedPrefix, "/") + "/"
	if !strings.HasPrefix(normalizedValue, prefixWithSlash) {
		return value, false
	}
	remainder := strings.TrimPrefix(normalizedValue, prefixWithSlash)
	return path.Join(pathClean(replacement), remainder), true
}

func pathClean(value string) string {
	return path.Clean(filepath.ToSlash(value))
}

func parseConfigJSON(content []byte) (map[string]any, error) {
	var parsed map[string]any
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, fmt.Errorf("parse openclaw config: %w", err)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return parsed, nil
}

func rewriteConfig(parsed map[string]any) ([]byte, error) {
	normalized, err := json.MarshalIndent(parsed, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("marshal openclaw config: %w", err)
	}
	normalized = append(normalized, '\n')
	return normalized, nil
}

type llmOverrides struct {
	BaseURL   string
	APIKey    string
	APIKeySet bool
	ModelIDs  []string
}

func readLLMOverridesFromEnv() (llmOverrides, bool, error) {
	var overrides llmOverrides

	if raw := strings.TrimSpace(os.Getenv("CLAWMANAGER_LLM_MODEL")); raw != "" {
		modelIDs, err := parseModelIDs(raw)
		if err != nil {
			return llmOverrides{}, false, err
		}
		overrides.ModelIDs = modelIDs
	}

	overrides.BaseURL = firstNonEmptyEnv("CLAWMANAGER_LLM_BASE_URL")
	overrides.APIKey, overrides.APIKeySet = firstLookupEnv("CLAWMANAGER_LLM_API_KEY")

	has := overrides.BaseURL != "" || overrides.APIKeySet || len(overrides.ModelIDs) > 0
	return overrides, has, nil
}

func parseModelIDs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	if strings.HasPrefix(raw, "[") {
		var parsed []any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			modelIDs := parseDelimitedModelIDs(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
			if len(modelIDs) == 0 {
				return nil, fmt.Errorf("parse CLAWMANAGER_LLM_MODEL array: %w", err)
			}
			return modelIDs, nil
		}
		modelIDs := uniqueNonEmptyModelIDs(parsed)
		if len(modelIDs) == 0 {
			return nil, fmt.Errorf("parse CLAWMANAGER_LLM_MODEL array: no model ids found")
		}
		return modelIDs, nil
	}

	return []string{raw}, nil
}

func parseDelimitedModelIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]any, 0, len(parts))
	for _, part := range parts {
		id := strings.Trim(strings.TrimSpace(part), `"'`)
		if id == "" {
			continue
		}
		values = append(values, id)
	}
	return uniqueNonEmptyModelIDs(values)
}

func uniqueNonEmptyModelIDs(values []any) []string {
	seen := make(map[string]struct{}, len(values))
	modelIDs := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(fmt.Sprint(value))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		modelIDs = append(modelIDs, id)
	}
	return modelIDs
}

// normalizeLLMConfigContent writes baseUrl / apiKey / models / primary into
// cfg based on environment-provided overrides. When no overrides exist for
// a given field, the corresponding subtree is left untouched.
func normalizeLLMConfigContent(cfg map[string]any, overrides llmOverrides) {
	models := ensureObject(cfg, "models")
	providers := ensureObject(models, "providers")
	providerName := autoProviderName
	provider := ensureObject(providers, providerName)

	if overrides.BaseURL != "" {
		provider["baseUrl"] = overrides.BaseURL
	}
	if overrides.APIKeySet {
		provider["apiKey"] = overrides.APIKey
	}
	if strings.TrimSpace(stringValue(provider["api"])) == "" {
		provider["api"] = "openai-completions"
	}
	if strings.TrimSpace(stringValue(provider["auth"])) == "" && strings.TrimSpace(overrides.APIKey) != "" {
		provider["auth"] = "api-key"
	}
	if len(overrides.ModelIDs) > 0 {
		provider["models"] = buildProviderModels(provider["models"], overrides.ModelIDs)

		agents := ensureObject(cfg, "agents")
		defaults := ensureObject(agents, "defaults")
		model := ensureObject(defaults, "model")
		model["primary"] = qualifiedModelID(providerName, overrides.ModelIDs[0])
		defaults["models"] = buildAgentModels(defaults["models"], providerName, overrides.ModelIDs)
	}
}

func normalizeProviderAuthContracts(cfg map[string]any) {
	providers, ok := nestedMap(cfg, "models", "providers")
	if !ok {
		return
	}
	for _, raw := range providers {
		provider, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(provider["auth"])) != "" {
			continue
		}
		if strings.TrimSpace(stringValue(provider["apiKey"])) == "" {
			continue
		}
		provider["auth"] = "api-key"
	}
}

func buildProviderModels(existing any, modelIDs []string) []any {
	byID := indexModelsByID(existing)
	models := make([]any, 0, len(modelIDs))
	for _, id := range modelIDs {
		if current, ok := byID[id]; ok {
			cloned := cloneMap(current)
			cloned["id"] = id
			if strings.EqualFold(id, "auto") || strings.TrimSpace(stringValue(cloned["name"])) == "" {
				cloned["name"] = displayModelName(id)
			}
			models = append(models, cloned)
			continue
		}
		models = append(models, defaultProviderModel(id))
	}
	return models
}

func indexModelsByID(existing any) map[string]map[string]any {
	items, ok := existing.([]any)
	if !ok {
		return map[string]map[string]any{}
	}

	index := make(map[string]map[string]any, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(stringValue(model["id"]))
		if id == "" {
			continue
		}
		index[id] = model
	}
	return index
}

func buildAgentModels(existing any, providerName string, modelIDs []string) map[string]any {
	current, _ := existing.(map[string]any)
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		key := qualifiedModelID(providerName, id)
		if current != nil {
			if value, ok := current[key]; ok {
				models[key] = value
				continue
			}
		}
		models[key] = map[string]any{}
	}
	return models
}

func defaultProviderModel(id string) map[string]any {
	return map[string]any{
		"id":        id,
		"name":      displayModelName(id),
		"reasoning": false,
		"input": []any{
			"text",
		},
		"cost": map[string]any{
			"input":      0,
			"output":     0,
			"cacheRead":  0,
			"cacheWrite": 0,
		},
		"contextWindow": 1000000,
		"maxTokens":     65536,
	}
}

func qualifiedModelID(providerName, id string) string {
	return providerName + "/" + id
}

func displayModelName(id string) string {
	if strings.EqualFold(id, "auto") {
		return "Auto"
	}
	return id
}

func ensureObject(parent map[string]any, key string) map[string]any {
	if current, ok := parent[key].(map[string]any); ok {
		return current
	}
	current := map[string]any{}
	parent[key] = current
	return current
}

func cloneMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func stringValue(value any) string {
	switch raw := value.(type) {
	case string:
		return raw
	case nil:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstLookupEnv(keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
	}
	return "", false
}
