package managedmodelrefs

import (
	"fmt"
	"strings"
)

// ModelID and ProviderID share Normalize's deterministic wire identity.
func ModelID(id string) string {
	if id == "" || safeToken(id, true) {
		return id
	}
	return Alias(id)
}
func ProviderID(id string) string {
	if safeToken(id, false) {
		return id
	}
	return providerAlias(id)
}
func Ref(provider, model string) string { return ProviderID(provider) + "/" + ModelID(model) }

func managedProvider(provider map[string]any, baseURL string, apiKeys []string) bool {
	if provider == nil || strings.TrimSpace(baseURL) == "" ||
		strings.TrimRight(text(provider["baseUrl"]), "/") != strings.TrimRight(baseURL, "/") {
		return false
	}
	// Compatibility API for normalization-only callers; production supplies a key.
	if len(apiKeys) == 0 {
		return true
	}
	key, literal := provider["apiKey"].(string)
	return literal && key == apiKeys[0]
}

// PrepareProviders validates the entire batch before moving normalized providers
// back to their input names for a lossless merge. auto is the reserved legacy
// managed provider and retains its connection/token rotation semantics.
func PrepareProviders(config map[string]any, names []string, baseURL, apiKey string) error {
	providers := object(object(config["models"])["providers"])
	moves := map[string]string{}
	requested := map[string]bool{}
	for _, name := range names {
		requested[name] = true
	}
	for _, name := range names {
		canonical := ProviderID(name)
		raw, rawExists := providers[name]
		saved, savedExists := providers[canonical]
		if name != "auto" && rawExists && !managedProvider(object(raw), baseURL, []string{apiKey}) {
			return fmt.Errorf("managed model provider conflicts with existing provider %q", name)
		}
		if canonical == name || !savedExists {
			continue
		}
		if rawExists || requested[canonical] || !managedProvider(object(saved), baseURL, []string{apiKey}) {
			return fmt.Errorf("managed model provider alias conflicts with existing provider %q", canonical)
		}
		moves[name] = canonical
	}
	for raw, canonical := range moves {
		providers[raw] = providers[canonical]
		delete(providers, canonical)
	}
	return nil
}

// ManagedProviderNames includes proven owned providers and validated destinations.
// Never use unverified URL-only matches to prune custom picker settings.
func ManagedProviderNames(config map[string]any, names []string, baseURL, apiKey string) map[string]bool {
	managed := map[string]bool{}
	for name, raw := range object(object(config["models"])["providers"]) {
		if managedProvider(object(raw), baseURL, []string{apiKey}) {
			managed[name] = true
			managed[ProviderID(name)] = true
		}
	}
	for _, name := range names {
		managed[name] = true
		managed[ProviderID(name)] = true
	}
	return managed
}

func MergeAgentEntries(existing any, desired map[string]any, managed map[string]bool) map[string]any {
	out := map[string]any{}
	for ref, settings := range object(existing) {
		provider, _, qualified := strings.Cut(ref, "/")
		if !qualified || !managed[provider] {
			out[ref] = settings
		}
	}
	for ref, settings := range desired {
		out[ref] = settings
	}
	return out
}
