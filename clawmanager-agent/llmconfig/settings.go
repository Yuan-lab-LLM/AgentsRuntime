package llmconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const (
	ProviderModelsEnv   = "CLAWMANAGER_LLM_PROVIDER_MODELS"
	LegacyModelsEnv     = "CLAWMANAGER_LLM_MODEL"
	ReasoningEnv        = "CLAWMANAGER_LLM_REASONING"
	ReasoningControlEnv = "CLAWMANAGER_LLM_REASONING_CONTROL"
)

var (
	baseURLEnvNames    = []string{"CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"}
	defaultAPIKeyNames = []string{"CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"}
	legacyModelNames   = []string{LegacyModelsEnv, "OPENAI_MODEL"}
)

type ResolveOptions struct {
	APIKeyEnvNames    []string
	LegacyModelPrefix []string
}

type Settings struct {
	Vision           map[string]bool
	BaseURL          string
	APIKey           string
	APIKeySet        bool
	APIKeySource     string
	ModelIDs         []string
	ModelsQualified  bool
	ModelSource      string
	Reasoning        map[string]bool
	ReasoningControl map[string]string
}

type ModelRef struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Qualified  string `json:"qualified_id"`
}

type ProviderModels struct {
	ProviderID string   `json:"provider_id"`
	ModelIDs   []string `json:"model_ids"`
}

type Canonical struct {
	Vision           map[string]bool   `json:"vision,omitempty"`
	BaseURL          string            `json:"base_url,omitempty"`
	APIKeySet        bool              `json:"api_key_set"`
	APIKeySource     string            `json:"api_key_env,omitempty"`
	ModelIDs         []string          `json:"model_ids"`
	ModelsQualified  bool              `json:"models_qualified"`
	ModelSource      string            `json:"model_source,omitempty"`
	Models           []ModelRef        `json:"models"`
	Providers        []ProviderModels  `json:"providers"`
	DefaultProvider  string            `json:"default_provider,omitempty"`
	DefaultModel     string            `json:"default_model,omitempty"`
	Reasoning        map[string]bool   `json:"reasoning,omitempty"`
	ReasoningControl map[string]string `json:"reasoning_control,omitempty"`
}

type envLookup func(names ...string) (name, value string, ok bool)

func FromGatewayConfig(cfg gateway.Config) Settings {
	return Settings{
		BaseURL:          strings.TrimRight(strings.TrimSpace(cfg.LLMBaseURL), "/"),
		APIKey:           cfg.LLMAPIKey,
		APIKeySet:        cfg.LLMAPIKeySet,
		APIKeySource:     cfg.LLMAPIKeyEnvName,
		ModelIDs:         append([]string(nil), cfg.LLMModelIDs...),
		ModelsQualified:  cfg.LLMModelsQualified,
		Vision:           cloneBoolMap(cfg.LLMVision),
		Reasoning:        cloneBoolMap(cfg.LLMReasoning),
		ReasoningControl: cloneStringMap(cfg.LLMReasoningControl),
	}
}

func (settings Settings) ApplyTo(cfg gateway.Config) gateway.Config {
	cfg.LLMBaseURL = settings.BaseURL
	cfg.LLMAPIKey = settings.APIKey
	cfg.LLMAPIKeySet = settings.APIKeySet
	cfg.LLMAPIKeyEnvName = settings.APIKeySource
	cfg.LLMModelIDs = append([]string(nil), settings.ModelIDs...)
	cfg.LLMModelsQualified = settings.ModelsQualified
	cfg.LLMVision = cloneBoolMap(settings.Vision)
	cfg.LLMReasoning = cloneBoolMap(settings.Reasoning)
	cfg.LLMReasoningControl = cloneStringMap(settings.ReasoningControl)
	return cfg
}

func ResolveGateway(cfg gateway.Config, req gateway.CreateGatewayRequest, options ResolveOptions) (Settings, error) {
	return resolve(FromGatewayConfig(cfg), func(names ...string) (string, string, bool) {
		return gateway.RequestEnvEntry(req, names...)
	}, options, true)
}

func LoadFromEnv(options ResolveOptions) (Settings, error) {
	return resolve(Settings{}, func(names ...string) (string, string, bool) {
		for _, name := range names {
			if value, ok := os.LookupEnv(name); ok {
				return name, value, true
			}
		}
		return "", "", false
	}, options, false)
}

func resolve(initial Settings, lookup envLookup, options ResolveOptions, emptyAPIKeyIsSet bool) (Settings, error) {
	resolved := initial

	if _, value, ok := firstNonBlank(lookup, baseURLEnvNames...); ok {
		resolved.BaseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}

	apiKeyNames := options.APIKeyEnvNames
	if len(apiKeyNames) == 0 {
		apiKeyNames = defaultAPIKeyNames
	}
	var apiKeySource, apiKeyValue string
	var apiKeyExists bool
	if emptyAPIKeyIsSet {
		apiKeySource, apiKeyValue, apiKeyExists = lookup(apiKeyNames...)
	} else {
		apiKeySource, apiKeyValue, apiKeyExists = firstNonBlank(lookup, apiKeyNames...)
	}
	if apiKeyExists {
		resolved.APIKey = strings.TrimSpace(apiKeyValue)
		resolved.APIKeySet = true
		resolved.APIKeySource = apiKeySource
	}

	if source, raw, ok := firstNonBlank(lookup, ProviderModelsEnv); ok {
		modelIDs, err := parseModelIDs(raw, source)
		if err != nil {
			return Settings{}, err
		}
		resolved.ModelIDs = modelIDs
		resolved.ModelsQualified = true
		resolved.ModelSource = source
	} else if source, raw, ok := firstNonBlank(lookup, legacyModelNames...); ok {
		modelIDs, err := parseModelIDs(raw, source)
		if err != nil {
			return Settings{}, err
		}
		resolved.ModelIDs = modelIDs
		resolved.ModelsQualified = false
		resolved.ModelSource = source
	}

	if _, raw, ok := lookup("CLAWMANAGER_LLM_VISION"); ok {
		vision, err := gateway.ParseVisionCapabilities(raw)
		if err != nil {
			return Settings{}, err
		}
		resolved.Vision = vision
	}
	if source, raw, ok := firstNonBlank(lookup, ReasoningEnv); ok {
		reasoning, err := parseReasoning(raw, source)
		if err != nil {
			return Settings{}, err
		}
		resolved.Reasoning = reasoning
	}
	if source, raw, ok := firstNonBlank(lookup, ReasoningControlEnv); ok {
		controls, err := parseReasoningControl(raw, source)
		if err != nil {
			return Settings{}, err
		}
		resolved.ReasoningControl = controls
	}
	if !resolved.ModelsQualified && len(resolved.ModelIDs) > 0 && len(options.LegacyModelPrefix) > 0 {
		resolved.ModelIDs = prependUnique(options.LegacyModelPrefix, resolved.ModelIDs)
	}

	return resolved, nil
}

func parseModelIDs(raw, source string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if source == "" {
		source = "LLM model configuration"
	}
	if strings.HasPrefix(raw, "[") {
		var parsed []any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			modelIDs := parseDelimitedModelIDs(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
			if len(modelIDs) == 0 {
				return nil, fmt.Errorf("parse %s array: %w", source, err)
			}
			return modelIDs, nil
		}
		modelIDs := uniqueModelIDs(parsed)
		if len(modelIDs) == 0 {
			return nil, fmt.Errorf("parse %s array: no model ids found", source)
		}
		return modelIDs, nil
	}
	return []string{raw}, nil
}

func parseReasoning(raw, source string) (map[string]bool, error) {
	settings := map[string]bool{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	return settings, nil
}

func parseReasoningControl(raw, source string) (map[string]string, error) {
	settings := map[string]string{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	return settings, nil
}

func (settings Settings) ModelRefs(fallbackProvider string) []ModelRef {
	fallbackProvider = strings.TrimSpace(fallbackProvider)
	if fallbackProvider == "" {
		fallbackProvider = "auto"
	}
	refs := make([]ModelRef, 0, len(settings.ModelIDs))
	seen := make(map[string]struct{}, len(settings.ModelIDs))
	for _, raw := range settings.ModelIDs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		providerID := fallbackProvider
		modelID := raw
		if settings.ModelsQualified {
			provider, model, qualified := strings.Cut(raw, "/")
			provider = strings.TrimSpace(provider)
			model = strings.TrimSpace(model)
			if qualified && provider != "" && model != "" {
				providerID = provider
				modelID = model
			}
		}
		qualifiedID := providerID + "/" + modelID
		if _, exists := seen[qualifiedID]; exists {
			continue
		}
		seen[qualifiedID] = struct{}{}
		refs = append(refs, ModelRef{ProviderID: providerID, ModelID: modelID, Qualified: qualifiedID})
	}
	return refs
}

func GroupModelRefs(refs []ModelRef) []ProviderModels {
	groups := make([]ProviderModels, 0)
	indexes := make(map[string]int)
	for _, ref := range refs {
		index, exists := indexes[ref.ProviderID]
		if !exists {
			index = len(groups)
			indexes[ref.ProviderID] = index
			groups = append(groups, ProviderModels{ProviderID: ref.ProviderID})
		}
		groups[index].ModelIDs = append(groups[index].ModelIDs, ref.ModelID)
	}
	return groups
}

func (settings Settings) Canonical(fallbackProvider string) Canonical {
	refs := settings.ModelRefs(fallbackProvider)
	canonical := Canonical{
		BaseURL:          settings.BaseURL,
		APIKeySet:        settings.APIKeySet,
		APIKeySource:     settings.APIKeySource,
		ModelIDs:         append([]string(nil), settings.ModelIDs...),
		ModelsQualified:  settings.ModelsQualified,
		Vision:           cloneBoolMap(settings.Vision),
		ModelSource:      settings.ModelSource,
		Models:           refs,
		Providers:        GroupModelRefs(refs),
		Reasoning:        cloneBoolMap(settings.Reasoning),
		ReasoningControl: cloneStringMap(settings.ReasoningControl),
	}
	if len(refs) > 0 {
		canonical.DefaultProvider = refs[0].ProviderID
		canonical.DefaultModel = refs[0].ModelID
	}
	return canonical
}

func firstNonBlank(lookup envLookup, names ...string) (name, value string, ok bool) {
	for _, name := range names {
		matched, value, exists := lookup(name)
		if exists && strings.TrimSpace(value) != "" {
			return matched, value, true
		}
	}
	return "", "", false
}

func prependUnique(prefix, values []string) []string {
	combined := append(append([]string(nil), prefix...), values...)
	items := make([]any, 0, len(combined))
	for _, value := range combined {
		items = append(items, value)
	}
	return uniqueModelIDs(items)
}

func parseDelimitedModelIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]any, 0, len(parts))
	for _, part := range parts {
		id := strings.Trim(strings.TrimSpace(part), `"'`)
		if id != "" {
			values = append(values, id)
		}
	}
	return uniqueModelIDs(values)
}

func uniqueModelIDs(values []any) []string {
	seen := make(map[string]struct{}, len(values))
	modelIDs := make([]string, 0, len(values))
	for _, value := range values {
		id := ""
		switch item := value.(type) {
		case map[string]any:
			for _, key := range []string{"id", "model", "default", "name"} {
				if candidate, exists := item[key]; exists {
					id = strings.TrimSpace(fmt.Sprint(candidate))
					if id != "" {
						break
					}
				}
			}
		default:
			id = strings.TrimSpace(fmt.Sprint(value))
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		modelIDs = append(modelIDs, id)
	}
	return modelIDs
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
