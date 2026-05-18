package configmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	agentsKey         = "agents"
	defaultsKey       = "defaults"
	defaultModelKey   = "model"
	modelsKey         = "models"
	providersKey      = "providers"
	autoProviderKey   = "auto"
	baseURLKey        = "baseUrl"
	apiKeyKey         = "apiKey"
	providerModelsKey = "models"
)

type modelGuardBaseline struct {
	// Deep clone of models.providers when it existed at capture; nil otherwise.
	providers map[string]any

	agentsDefaultsModel       any
	agentsDefaultsModelExists bool
}

// CaptureModelBaseline snapshots model-related fields from the current
// openclaw.json, which are treated as locked initial model config.
func (m *Manager) CaptureModelBaseline() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	content, err := os.ReadFile(m.cfg.OpenClawConfigPath)
	if err != nil {
		return fmt.Errorf("read openclaw config for model baseline: %w", err)
	}
	parsed, err := parseConfigJSON(content)
	if err != nil {
		return fmt.Errorf("parse openclaw config for model baseline: %w", err)
	}

	return m.setModelBaselineLocked(parsed)
}

func (m *Manager) setModelBaselineLocked(parsed map[string]any) error {
	var baseline modelGuardBaseline
	if modelsRoot, ok := parsed[modelsKey].(map[string]any); ok {
		if raw, ok := modelsRoot[providersKey].(map[string]any); ok && raw != nil {
			clone, err := cloneJSONValue(raw)
			if err != nil {
				return fmt.Errorf("clone model baseline providers: %w", err)
			}
			baseline.providers = clone.(map[string]any)
		}
	}
	if current, exists := nestedValue(parsed, agentsKey, defaultsKey, defaultModelKey); exists {
		cloned, err := cloneJSONValue(current)
		if err != nil {
			return fmt.Errorf("clone agents.defaults.model baseline: %w", err)
		}
		baseline.agentsDefaultsModel = cloned
		baseline.agentsDefaultsModelExists = true
	}
	m.modelGuardSnapshot = baseline
	m.modelBaselineSet = true
	return nil
}

// EnforceModelBaseline restores model-related fields to the captured
// baseline when it has been modified. The returned bool reports whether
// a restore write happened.
func (m *Manager) EnforceModelBaseline() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.modelBaselineSet {
		return false, nil
	}

	content, err := os.ReadFile(m.cfg.OpenClawConfigPath)
	if err != nil {
		return false, fmt.Errorf("read openclaw config for model check: %w", err)
	}
	parsed, err := parseConfigJSON(content)
	if err != nil {
		return false, fmt.Errorf("parse openclaw config for model check: %w", err)
	}

	changed := false

	if b := m.modelGuardSnapshot.providers; b != nil {
		ch, err := reconcileProvidersAgainstBaseline(parsed, b)
		if err != nil {
			return false, err
		}
		if ch {
			changed = true
		}
	}

	if ch, err := enforceAgentsDefaultsModelBaseline(parsed, m.modelGuardSnapshot); err != nil {
		return false, err
	} else if ch {
		changed = true
	}

	if !changed {
		return false, nil
	}

	rewritten, err := rewriteConfig(parsed)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(m.cfg.OpenClawConfigPath, rewritten, 0o600); err != nil {
		return false, fmt.Errorf("write restored model to openclaw config: %w", err)
	}

	return true, nil
}

func reconcileProvidersAgainstBaseline(parsed map[string]any, baselineProviders map[string]any) (bool, error) {
	modelsRoot, ok := parsed[modelsKey].(map[string]any)
	if !ok {
		modelsRoot = map[string]any{}
		parsed[modelsKey] = modelsRoot
	}
	prov, ok := modelsRoot[providersKey].(map[string]any)
	if !ok {
		prov = map[string]any{}
		modelsRoot[providersKey] = prov
	}

	changed := false

	for name := range prov {
		if _, ok := baselineProviders[name]; !ok {
			delete(prov, name)
			changed = true
		}
	}

	for name, want := range baselineProviders {
		if _, ok := prov[name]; !ok {
			cloned, err := cloneJSONValue(want)
			if err != nil {
				return false, fmt.Errorf("restore missing provider %q: %w", name, err)
			}
			prov[name] = cloned
			changed = true
			continue
		}
		if name == autoProviderKey {
			ch, err := enforceAutoProviderInPlace(prov, want)
			if err != nil {
				return false, err
			}
			if ch {
				changed = true
			}
			continue
		}
		if !jsonValuesEqual(prov[name], want) {
			cloned, err := cloneJSONValue(want)
			if err != nil {
				return false, fmt.Errorf("restore provider %q: %w", name, err)
			}
			prov[name] = cloned
			changed = true
		}
	}

	return changed, nil
}

func enforceAutoProviderInPlace(prov map[string]any, baselineRaw any) (bool, error) {
	currentRaw := prov[autoProviderKey]
	current, ok := currentRaw.(map[string]any)
	if !ok {
		cloned, err := cloneJSONValue(baselineRaw)
		if err != nil {
			return false, err
		}
		prov[autoProviderKey] = cloned
		return true, nil
	}
	baseline, ok := baselineRaw.(map[string]any)
	if !ok {
		return false, nil
	}

	changed := false

	if v, ok := baseline[baseURLKey]; ok {
		if !jsonValuesEqual(current[baseURLKey], v) {
			cloned, err := cloneJSONValue(v)
			if err != nil {
				return false, err
			}
			current[baseURLKey] = cloned
			changed = true
		}
	}
	if v, ok := baseline[apiKeyKey]; ok {
		if !jsonValuesEqual(current[apiKeyKey], v) {
			cloned, err := cloneJSONValue(v)
			if err != nil {
				return false, err
			}
			current[apiKeyKey] = cloned
			changed = true
		}
	}

	baseModels, ok := baseline[providerModelsKey].([]any)
	if !ok || len(baseModels) == 0 {
		return changed, nil
	}

	order, byID, err := indexBaselineModelEntries(baseModels)
	if err != nil {
		return false, err
	}
	merged, ch, err := mergeAutoModelsPreservingCosmetics(current[providerModelsKey], order, byID)
	if err != nil {
		return false, err
	}
	if ch {
		current[providerModelsKey] = merged
		changed = true
	}
	return changed, nil
}

func indexBaselineModelEntries(baseModels []any) (order []string, byID map[string]map[string]any, err error) {
	byID = make(map[string]map[string]any)
	for _, raw := range baseModels {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(m["id"]))
		if id == "" {
			continue
		}
		order = append(order, id)
		cloned, err := cloneJSONValue(m)
		if err != nil {
			return nil, nil, err
		}
		byID[id] = cloned.(map[string]any)
	}
	return order, byID, nil
}

func mergeAutoModelsPreservingCosmetics(currentRaw any, order []string, baselineByID map[string]map[string]any) ([]any, bool, error) {
	allowed := make(map[string]struct{}, len(order))
	for _, id := range order {
		allowed[id] = struct{}{}
	}

	currentList, _ := currentRaw.([]any)
	currentByID := make(map[string]map[string]any)
	for _, raw := range currentList {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(m["id"]))
		if id == "" {
			continue
		}
		cloned, err := cloneJSONValue(m)
		if err != nil {
			return nil, false, err
		}
		currentByID[id] = cloned.(map[string]any)
	}

	changed := false
	for id := range currentByID {
		if _, ok := allowed[id]; !ok {
			changed = true
		}
	}

	out := make([]any, 0, len(order))
	for _, id := range order {
		if cur, ok := currentByID[id]; ok {
			if fmt.Sprint(cur["id"]) != id {
				cur["id"] = id
				changed = true
			}
			out = append(out, cur)
			continue
		}
		cloned, err := cloneJSONValue(baselineByID[id])
		if err != nil {
			return nil, false, err
		}
		out = append(out, cloned)
		changed = true
	}

	if len(currentList) != len(out) {
		changed = true
	} else {
		for i := range out {
			if !jsonValuesEqual(currentList[i], out[i]) {
				changed = true
				break
			}
		}
	}
	return out, changed, nil
}

func enforceAgentsDefaultsModelBaseline(parsed map[string]any, b modelGuardBaseline) (bool, error) {
	current, exists := nestedValue(parsed, agentsKey, defaultsKey, defaultModelKey)
	if !b.agentsDefaultsModelExists {
		if exists {
			deleteNestedValue(parsed, agentsKey, defaultsKey, defaultModelKey)
			return true, nil
		}
		return false, nil
	}
	if !exists || !jsonValuesEqual(current, b.agentsDefaultsModel) {
		cloned, err := cloneJSONValue(b.agentsDefaultsModel)
		if err != nil {
			return false, err
		}
		setNestedValue(parsed, cloned, agentsKey, defaultsKey, defaultModelKey)
		return true, nil
	}
	return false, nil
}

func nestedValue(root map[string]any, path ...string) (any, bool) {
	current := any(root)
	for idx, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		if idx == len(path)-1 {
			return next, true
		}
		current = next
	}
	return nil, false
}

func nestedMap(root map[string]any, path ...string) (map[string]any, bool) {
	current := root
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func setNestedValue(root map[string]any, value any, path ...string) {
	if len(path) == 0 {
		return
	}
	parent := root
	for _, key := range path[:len(path)-1] {
		parent = ensureObject(parent, key)
	}
	parent[path[len(path)-1]] = value
}

func deleteNestedValue(root map[string]any, path ...string) {
	if len(path) == 0 {
		return
	}
	parent := root
	for _, key := range path[:len(path)-1] {
		next, ok := parent[key].(map[string]any)
		if !ok {
			return
		}
		parent = next
	}
	delete(parent, path[len(path)-1])
}

func cloneJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func jsonValuesEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}
