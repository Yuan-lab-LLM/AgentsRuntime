package hermes

import (
	"fmt"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"gopkg.in/yaml.v3"
)

// Pin only the vision auxiliary task. Preserve other auxiliary settings,
// comments and unrelated user configuration. Legacy requests remain unchanged.
func mergeManagedVisionYAML(base string, cfg gateway.Config) (string, error) {
	if cfg.LLMVision == nil {
		return base, nil
	}
	if strings.TrimSpace(cfg.LLMBaseURL) == "" {
		return "", fmt.Errorf("managed vision requires gateway base URL")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(base), &doc); err != nil {
		return "", fmt.Errorf("parse Hermes vision config: %w", err)
	}
	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if err := applyManagedVisionYAML(doc.Content[0], cfg); err != nil {
		return "", err
	}
	raw, err := yaml.Marshal(&doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Share the managed vision leaves with the native protected and Desktop Web
// writers, which retain their own inode-safe parsing and publication paths.
func applyManagedVisionYAML(root *yaml.Node, cfg gateway.Config) error {
	if cfg.LLMVision == nil {
		return nil
	}
	if strings.TrimSpace(cfg.LLMBaseURL) == "" {
		return fmt.Errorf("managed vision requires gateway base URL")
	}
	aux, err := visionYAMLMap(root, "auxiliary")
	if err != nil {
		return err
	}
	vision, err := visionYAMLMap(aux, "vision")
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{"provider", "custom"}, {"model", "auto"}, {"base_url", cfg.LLMBaseURL}, {"api_key", ""}, {"api_mode", "chat_completions"}} {
		visionYAMLSet(vision, pair[0], &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pair[1]})
	}
	// Do not let a historical extra_body override model or routing parameters.
	visionYAMLSet(vision, "extra_body", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	return nil
}

func visionYAMLMap(parent *yaml.Node, key string) (*yaml.Node, error) {
	if parent.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Hermes vision parent must be a mapping")
	}
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			value := parent.Content[i+1]
			if value.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("Hermes %s must be a mapping", key)
			}
			return value, nil
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	visionYAMLSet(parent, key, value)
	return value, nil
}
func visionYAMLSet(parent *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = value
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
