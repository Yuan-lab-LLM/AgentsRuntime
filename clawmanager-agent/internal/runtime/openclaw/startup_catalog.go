package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
	"strings"
)

// Prune proven Gateway-owned leaves only, never sessions or foreign providers.
func pruneStartupModelCatalog(config map[string]any, owned map[string]bool, refs []llmconfig.ModelRef) {
	desiredProviders, allowed := map[string]bool{}, map[string]bool{}
	for _, ref := range refs {
		desiredProviders[ref.ProviderID] = true
		allowed[ref.Qualified] = true
	}
	providers := ensureObject(ensureObject(config, "models"), "providers")
	for id := range providers {
		if owned[id] && !desiredProviders[id] {
			delete(providers, id)
		}
	}
	defaults := ensureObject(ensureObject(config, "agents"), "defaults")
	stale := func(ref string) bool {
		provider, _, _ := strings.Cut(ref, "/")
		return owned[provider] && !allowed[ref]
	}
	for _, field := range []string{"model", "imageModel"} {
		switch value := defaults[field].(type) {
		case string:
			if stale(value) {
				delete(defaults, field)
			}
		case map[string]any:
			if primary, ok := value["primary"].(string); ok && stale(primary) {
				delete(value, "primary")
			}
			if list, ok := value["fallbacks"].([]any); ok {
				keep := []any{}
				for _, raw := range list {
					ref, _ := raw.(string)
					if !stale(ref) {
						keep = append(keep, raw)
					}
				}
				value["fallbacks"] = keep
			}
		}
	}
	if current, ok := defaults["models"].(map[string]any); ok {
		for ref := range current {
			if stale(ref) {
				delete(current, ref)
			}
		}
	}
	if policy, ok := defaults["modelPolicy"].(map[string]any); ok {
		for _, field := range []string{"allow", "deny"} {
			if list, ok := policy[field].([]any); ok {
				keep, seen := []any{}, map[string]bool{}
				for _, raw := range list {
					ref, _ := raw.(string)
					if !stale(ref) {
						keep = append(keep, raw)
						seen[ref] = true
					}
				}
				if field == "allow" {
					for _, ref := range refs {
						if !seen[ref.Qualified] {
							keep = append(keep, ref.Qualified)
						}
					}
				}
				policy[field] = keep
			}
		}
	}
}
