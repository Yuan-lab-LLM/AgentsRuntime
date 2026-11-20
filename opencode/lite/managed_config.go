package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

// RenderManagedConfig is shared by Lite and the Pro startup command. A control
// plane document takes precedence over the legacy provider-qualified catalog.
// Malformed managed content must not silently switch to a different namespace.
func RenderManagedConfig(settings llmconfig.Settings, content string) ([]byte, error) {
	if settings.CatalogSynced {
		return RenderConfig(settings)
	}
	if strings.TrimSpace(content) == "" {
		return RenderConfig(settings)
	}
	if _, err := normalizeLLMSettings(settings); err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("invalid managed OpenCode config: %w", err)
	}
	providers, _ := doc["provider"].(map[string]any)
	provider, _ := providers[clawmanagerProviderID].(map[string]any)
	options, _ := provider["options"].(map[string]any)
	models, _ := provider["models"].(map[string]any)
	endpoint, _ := options["baseURL"].(string)
	key, _ := options["apiKey"].(string)
	if len(providers) != 1 || len(models) == 0 ||
		(endpoint != "{env:CLAWMANAGER_LLM_BASE_URL}" && strings.TrimRight(endpoint, "/") != strings.TrimRight(settings.BaseURL, "/")) ||
		key != "{env:CLAWMANAGER_LLM_API_KEY}" {
		return nil, fmt.Errorf("managed OpenCode config must use the instance Gateway provider and credential reference")
	}
	selected, _ := doc["model"].(string)
	if !strings.HasPrefix(selected, clawmanagerProviderID+"/") || models[strings.TrimPrefix(selected, clawmanagerProviderID+"/")] == nil {
		return nil, fmt.Errorf("managed OpenCode default model is missing from its catalog")
	}
	for id, raw := range models {
		model, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid managed OpenCode model entry")
		}
		if window := settings.ContextFor(clawmanagerProviderID, id); window > 0 {
			limit, _ := model["limit"].(map[string]any)
			if limit == nil {
				limit = map[string]any{}
			}
			limit["context"] = window
			model["limit"] = limit
		}
		if vision, managed := settings.Vision[id]; managed {
			capabilities, _ := model["capabilities"].(map[string]any)
			if capabilities == nil {
				capabilities = map[string]any{}
			}
			capabilities["input"] = gateway.VisionInputModalities(nil, vision)
			if _, ok := capabilities["output"]; !ok {
				capabilities["output"] = []string{"text"}
			}
			model["capabilities"] = capabilities
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// MergeManagedFile preserves non-managed settings/providers. It removes only
// old provider blocks that demonstrably use this instance's Gateway credential.
func MergeManagedFile(path string, raw []byte, settings llmconfig.Settings) ([]byte, error) {
	var desired map[string]any
	if err := json.Unmarshal(raw, &desired); err != nil {
		return nil, err
	}
	providers, _ := desired["provider"].(map[string]any)
	if len(providers) == 0 {
		return nil, fmt.Errorf("generated OpenCode provider map is empty")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to replace non-regular OpenCode config")
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return mergeManagedConfigData(previous, raw, settings)
}

func mergeManagedConfigData(previous, raw []byte, settings llmconfig.Settings) ([]byte, error) {
	var desired map[string]any
	if err := json.Unmarshal(raw, &desired); err != nil {
		return nil, err
	}
	providers, _ := desired["provider"].(map[string]any)
	if len(providers) == 0 {
		return nil, fmt.Errorf("generated OpenCode provider map is empty")
	}
	var old map[string]any
	if err := json.Unmarshal(previous, &old); err != nil || old == nil {
		return nil, fmt.Errorf("existing OpenCode config is not a JSON object; preserving it for manual repair")
	}
	oldProviders, _ := old["provider"].(map[string]any)
	if value, exists := old["provider"]; exists && value != nil && oldProviders == nil {
		return nil, fmt.Errorf("invalid existing OpenCode provider map; preserving config")
	}
	if oldProviders == nil {
		oldProviders = map[string]any{}
	}
	removed := map[string]bool{}
	providerIDs := make([]string, 0, len(providers))
	for id := range providers {
		removed[id] = true
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for id, value := range oldProviders {
		block, _ := value.(map[string]any)
		opts, _ := block["options"].(map[string]any)
		endpoint, _ := opts["baseURL"].(string)
		key, _ := opts["apiKey"].(string)
		credentialMatches := key == "{env:CLAWMANAGER_LLM_API_KEY}" || (settings.APIKeySet && key == settings.APIKey) ||
			(settings.APIKeySource != "" && key == "{env:"+settings.APIKeySource+"}")
		managed := (endpoint == "{env:CLAWMANAGER_LLM_BASE_URL}" ||
			(settings.BaseURL != "" && strings.TrimRight(endpoint, "/") == strings.TrimRight(settings.BaseURL, "/"))) && credentialMatches
		if _, conflict := providers[id]; conflict && !managed {
			return nil, fmt.Errorf("existing provider %q is not owned by this Gateway; preserving config for manual repair", id)
		}
		if managed {
			removed[id] = true
			delete(oldProviders, id)
		}
	}
	for id, provider := range providers {
		oldProviders[id] = provider
	}
	old["provider"] = oldProviders
	old["$schema"] = desired["$schema"]
	selected, _ := old["model"].(string)
	providerID, _, _ := strings.Cut(selected, "/")
	if selected == "" || removed[providerID] {
		if model, exists := desired["model"]; exists {
			old["model"] = model
		} else {
			delete(old, "model")
		}
	}
	for _, field := range []string{"enabled_providers", "disabled_providers"} {
		values, exists := old[field]
		if !exists {
			continue
		}
		list, ok := values.([]any)
		if !ok {
			return nil, fmt.Errorf("invalid existing OpenCode provider filter; preserving config")
		}
		kept := []any{}
		for _, value := range list {
			id, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("invalid existing OpenCode provider filter entry")
			}
			if !removed[id] {
				kept = append(kept, id)
			}
		}
		if field == "enabled_providers" {
			for _, id := range providerIDs {
				kept = append(kept, id)
			}
		}
		old[field] = kept
	}
	return json.MarshalIndent(old, "", "  ")
}

func writeConfigAtomic(path string, raw []byte, uid, gid int) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".opencode-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o640); err != nil {
		return err
	}
	if err := gateway.ChownWorkspace(file.Name(), uid, gid); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
