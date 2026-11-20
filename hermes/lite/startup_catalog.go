package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"gopkg.in/yaml.v3"
	"strings"
)

// Merge one YAML document instead of appending a duplicate providers/model
// mapping that would hide user providers in native YAML loaders.
func mergeHermesStartupYAML(base, block string, cfg gateway.Config) ([]byte, error) {
	document, err := parseDesktopWebYAML([]byte(base))
	if err != nil {
		return nil, err
	}
	managed, err := parseDesktopWebYAML([]byte(block))
	if err != nil {
		return nil, err
	}
	var incoming map[string]any
	if err := managed.Decode(&incoming); err != nil {
		return nil, err
	}
	providers, _ := incoming["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	if err := reconcileHermesModelCatalog(document.Content[0], providers, cfg); err != nil {
		return nil, err
	}
	preserveForeignDefaultModel(document.Content[0], managed.Content[0], cfg)
	mergeDesktopWebYAMLNode(document.Content[0], managed.Content[0])
	return yaml.Marshal(document)
}

func preserveForeignDefaultModel(root, overlay *yaml.Node, cfg gateway.Config) {
	model := desktopWebYAMLMappingValue(root, "model")
	provider := desktopWebYAMLMappingValue(model, "provider")
	if provider == nil {
		return
	}
	entry := desktopWebYAMLMappingValue(desktopWebYAMLMappingValue(root, "providers"), provider.Value)
	endpoint := desktopWebYAMLMappingValue(entry, "base_url")
	if endpoint == nil || strings.TrimSpace(endpoint.Value) == "" || strings.TrimRight(endpoint.Value, "/") == strings.TrimRight(cfg.LLMBaseURL, "/") {
		return
	}
	var kept []*yaml.Node
	for i := 0; i < len(overlay.Content); i += 2 {
		if overlay.Content[i].Value != "model" {
			kept = append(kept, overlay.Content[i], overlay.Content[i+1])
		}
	}
	overlay.Content = kept
}
