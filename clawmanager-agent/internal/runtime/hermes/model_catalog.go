package hermes

import (
	"fmt"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"gopkg.in/yaml.v3"
)

// Reconcile only Gateway-owned catalog leaves. Recursive YAML overlay alone
// retains revoked models forever. User providers and per-model metadata for
// current entries survive; session databases are never touched here.
func reconcileHermesModelCatalog(root *yaml.Node, desired map[string]any, cfg gateway.Config) error {
	providers := desktopWebYAMLMappingValue(root, "providers")
	if providers == nil {
		return nil
	}
	if providers.Kind != yaml.MappingNode {
		return fmt.Errorf("invalid Hermes provider catalog; preserving config")
	}
	var kept []*yaml.Node
	for index := 0; index < len(providers.Content); index += 2 {
		key, provider := providers.Content[index], providers.Content[index+1]
		endpoint := desktopWebYAMLMappingValue(provider, "base_url")
		credential := desktopWebYAMLMappingValue(provider, "key_env")
		literal := desktopWebYAMLMappingValue(provider, "api_key")
		endpointMatches := endpoint != nil && cfg.LLMBaseURL != "" && strings.TrimRight(endpoint.Value, "/") == strings.TrimRight(cfg.LLMBaseURL, "/")
		credentialMatches := credential != nil && (credential.Value == "OPENAI_API_KEY" || credential.Value == "CLAWMANAGER_LLM_API_KEY") || literal != nil && cfg.LLMAPIKeySet && cfg.LLMAPIKey != "" && literal.Value == cfg.LLMAPIKey
		owned := endpointMatches && credentialMatches
		incoming, current := desired[key.Value].(map[string]any)
		// The original single-provider writer omitted connection leaves on some
		// legacy clawmanager entries. Do not apply this exception to another URL.
		legacy := key.Value == "clawmanager" && endpoint == nil
		if current && !owned && !legacy {
			return fmt.Errorf("Hermes provider %q conflicts with user configuration; preserving config", key.Value)
		}
		if owned && !current {
			continue
		}
		if current {
			allowed, _ := incoming["models"].(map[string]any)
			catalog := desktopWebYAMLMappingValue(provider, "models")
			if catalog != nil {
				if catalog.Kind != yaml.MappingNode {
					return fmt.Errorf("invalid Hermes model catalog; preserving config")
				}
				var entries []*yaml.Node
				for entry := 0; entry < len(catalog.Content); entry += 2 {
					if _, valid := allowed[catalog.Content[entry].Value]; valid {
						if cfg.LLMCatalogSynced && cfg.LLMContextWindows[catalog.Content[entry].Value] == 0 {
							metadata := catalog.Content[entry+1]
							if metadata.Kind == yaml.MappingNode {
								var fields []*yaml.Node
								for i := 0; i < len(metadata.Content); i += 2 {
									if metadata.Content[i].Value != "context_length" {
										fields = append(fields, metadata.Content[i], metadata.Content[i+1])
									}
								}
								metadata.Content = fields
							}
						}
						entries = append(entries, catalog.Content[entry], catalog.Content[entry+1])
					}
				}
				catalog.Content = entries
			}
		}
		kept = append(kept, key, provider)
	}
	providers.Content = kept
	return nil
}
