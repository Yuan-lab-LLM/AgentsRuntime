// Package managedmodelrefs repairs only providers connected to the managed Gateway.
// The alias algorithm is a wire contract with ClawManager's RuntimeModelAlias.
package managedmodelrefs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func safeToken(s string, slash bool) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' || slash && c == '/' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/") && !strings.Contains(s, "//")
}

func Alias(name string) string {
	return fmt.Sprintf("cm-model-v1-%x", sha256.Sum256([]byte(strings.TrimSpace(name))))
}
func providerAlias(name string) string {
	return fmt.Sprintf("cm-provider-v1-%x", sha256.Sum256([]byte(name)))
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string           { s, _ := v.(string); return s }

// Normalize keeps model names for display, but rewrites IDs and every supported
// policy/selection reference. It never broadens policies or touches credentials.
func Normalize(config map[string]any, baseURL string, apiKeys ...string) (bool, error) {
	if strings.TrimSpace(baseURL) == "" {
		return false, nil
	}
	before, err := json.Marshal(config)
	if err != nil {
		return false, err
	}
	providers := object(object(config["models"])["providers"])
	replacements := map[string]string{}
	keys := make([]string, 0, len(providers))
	for key := range providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Validate every destination before mutating anything. Even a same-URL
	// destination may belong to another credential or carry different metadata.
	for _, old := range keys {
		provider := object(providers[old])
		if !managedProvider(provider, baseURL, apiKeys) {
			continue
		}
		next := ProviderID(old)
		if next != old {
			if _, exists := providers[next]; exists {
				return false, fmt.Errorf("managed model provider alias conflicts with existing provider %q", next)
			}
		}
		seen := map[string]bool{}
		list, _ := provider["models"].([]any)
		for _, raw := range list {
			id := ModelID(text(object(raw)["id"]))
			if id != "" && seen[id] {
				return false, fmt.Errorf("managed model alias conflicts in provider %q", old)
			}
			seen[id] = true
		}
		replacements[old] = next
	}
	rewrite := func(ref string) string {
		provider, model, ok := strings.Cut(ref, "/")
		next, managed := replacements[provider]
		if !ok || !managed {
			return ref
		}
		validWildcard := model == "*" || strings.HasSuffix(model, "/*") && safeToken(strings.TrimSuffix(model, "/*"), true)
		if !validWildcard && !safeToken(model, true) {
			// Never hash a wildcard into an exact grant. An invalid namespace pattern
			// needs explicit repair rather than silently changing its meaning.
			if strings.Contains(model, "*") {
				return ref
			}
			model = Alias(model)
		}
		return next + "/" + model
	}
	// Alias/parameter maps are identity-keyed too. Never silently discard one
	// entry when a legacy and normalized reference collide.
	agentConfig := object(config["agents"])
	agentEntries := []any{agentConfig["defaults"]}
	if list, ok := agentConfig["list"].([]any); ok {
		agentEntries = append(agentEntries, list...)
	}
	for _, raw := range agentEntries {
		seen := map[string]bool{}
		for ref := range object(object(raw)["models"]) {
			next := rewrite(ref)
			if seen[next] {
				return false, fmt.Errorf("managed agent model alias conflicts with existing entry %q", next)
			}
			seen[next] = true
		}
	}
	for _, old := range keys {
		provider := object(providers[old])
		if !managedProvider(provider, baseURL, apiKeys) {
			continue
		}
		next := old
		if !safeToken(old, false) {
			next = providerAlias(old)
		}
		replacements[old] = next
		list, _ := provider["models"].([]any)
		for _, raw := range list {
			model := object(raw)
			id := text(model["id"])
			if id == "" || safeToken(id, true) {
				continue
			}
			if text(model["name"]) == "" {
				model["name"] = id
			}
			model["id"] = Alias(id)
		}
		if next != old {
			providers[next] = provider
			delete(providers, old)
		}
	}
	var transform func(any) any
	transform = func(v any) any {
		switch value := v.(type) {
		case string:
			return rewrite(value)
		case []string:
			out := make([]string, len(value))
			for i, ref := range value {
				out[i] = rewrite(ref)
			}
			return out
		case []any:
			out := make([]any, len(value))
			for i, ref := range value {
				out[i] = transform(ref)
			}
			return out
		case map[string]any:
			for _, key := range []string{"primary", "fallbacks"} {
				if ref, exists := value[key]; exists {
					value[key] = transform(ref)
				}
			}
			return value
		}
		return v
	}
	rewriteAgent := func(agent map[string]any) {
		for _, key := range []string{"model", "imageModel"} {
			if v, exists := agent[key]; exists {
				agent[key] = transform(v)
			}
		}
		if heartbeat := object(agent["heartbeat"]); heartbeat != nil {
			if v, exists := heartbeat["model"]; exists {
				heartbeat["model"] = transform(v)
			}
		}
		if legacy := object(agent["models"]); legacy != nil {
			// Move complete entries so configured aliases/parameters are retained.
			refs := make([]string, 0, len(legacy))
			for ref := range legacy {
				refs = append(refs, ref)
			}
			sort.Strings(refs)
			for _, ref := range refs {
				next := rewrite(ref)
				if next != ref {
					if _, exists := legacy[next]; !exists {
						legacy[next] = legacy[ref]
					}
					delete(legacy, ref)
				}
			}
		}
		if policy := object(agent["modelPolicy"]); policy != nil {
			for _, key := range []string{"allow", "deny"} {
				if v, exists := policy[key]; exists {
					policy[key] = transform(v)
				}
			}
		}
	}
	agents := object(config["agents"])
	rewriteAgent(object(agents["defaults"]))
	if list, ok := agents["list"].([]any); ok {
		for _, raw := range list {
			rewriteAgent(object(raw))
		}
	}
	after, err := json.Marshal(config)
	return string(before) != string(after), err
}

// BackupOnce preserves the exact pre-migration file without overwriting an
// earlier backup. Missing files are new instances, not a migration.
func BackupOnce(path string) error {
	original, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path+".model-ref-v1.bak", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = file.Write(original); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
