package openclawcompat

import (
	"sort"
	"strings"
)

// Normalize81 applies the deterministic, allow-listed OpenClaw 7.1 -> 8.1
// configuration contract migration used by both normal gateway starts and the
// data-safe upgrade workflow. It is intentionally idempotent.
func Normalize81(config map[string]any) {
	if meta, ok := config["meta"].(map[string]any); ok {
		delete(meta, "lastTouchedAt")
	}

	migrateMemorySearch(config)
	agentDefaults := ensureObject(ensureObject(config, "agents"), "defaults")
	compaction := ensureObject(agentDefaults, "compaction")
	delete(compaction, "reserveTokens")
	delete(compaction, "reserveTokensFloor")
	delete(compaction, "maxHistoryShare")
	migrateModelPolicy(agentDefaults)

	commands := ensureObject(config, "commands")
	delete(commands, "ownerDisplay")
	delete(commands, "ownerDisplaySecret")

	gatewayConfig := ensureObject(config, "gateway")
	if tailscale, ok := gatewayConfig["tailscale"].(map[string]any); ok {
		delete(tailscale, "resetOnExit")
	}
	nodes := ensureObject(gatewayConfig, "nodes")
	legacyDenied := stringArray(nodes["denyCommands"])
	if len(legacyDenied) > 0 {
		nodeCommands := ensureObject(nodes, "commands")
		nodeCommands["deny"] = appendUniqueStrings(stringArray(nodeCommands["deny"]), legacyDenied...)
	}
	delete(nodes, "denyCommands")

	controlUI := ensureObject(gatewayConfig, "controlUi")
	delete(controlUI, "dangerouslyDisableDeviceAuth")
	delete(config, "cloudWorkers")
	delete(gatewayConfig, "roles")

	cron := objectValue(config["cron"])
	delete(cron, "runLog")
	delete(cron, "maxConcurrentRuns")

	if entries, ok := objectValue(config["plugins"])["entries"].(map[string]any); ok {
		for _, pluginID := range []string{"acpx", "phone-control"} {
			entry, _ := entries[pluginID].(map[string]any)
			if entry != nil && entry["enabled"] == false {
				delete(entries, pluginID)
			}
		}
	}

	if browser, ok := config["browser"].(map[string]any); ok {
		delete(browser, "color")
		if profiles, ok := browser["profiles"].(map[string]any); ok {
			for _, value := range profiles {
				if profile, ok := value.(map[string]any); ok {
					delete(profile, "color")
				}
			}
		}
	}
}

func migrateMemorySearch(config map[string]any) {
	rootSearch := ensureObject(ensureObject(config, "memory"), "search")
	mergeMissingObjectValues(rootSearch, objectValue(config["memorySearch"]))
	delete(config, "memorySearch")

	agents := ensureObject(config, "agents")
	defaults := ensureObject(agents, "defaults")
	mergeMissingObjectValues(rootSearch, objectValue(defaults["memorySearch"]))
	delete(defaults, "memorySearch")

	if list, ok := agents["list"].([]any); ok {
		for _, value := range list {
			agent, ok := value.(map[string]any)
			if !ok {
				continue
			}
			legacy := objectValue(agent["memorySearch"])
			if legacy != nil {
				mergeMissingObjectValues(ensureObject(ensureObject(agent, "memory"), "search"), legacy)
				delete(agent, "memorySearch")
			}
		}
	}
}

func migrateModelPolicy(agentDefaults map[string]any) {
	legacy, ok := agentDefaults["models"].(map[string]any)
	if !ok {
		return
	}
	refs := make([]string, 0, len(legacy))
	for ref := range legacy {
		if strings.TrimSpace(ref) != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	policy := ensureObject(agentDefaults, "modelPolicy")
	policy["allow"] = appendUniqueStrings(stringArray(policy["allow"]), refs...)
	delete(agentDefaults, "models")
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func mergeMissingObjectValues(target, source map[string]any) {
	for key, value := range source {
		if _, exists := target[key]; !exists {
			target[key] = value
		}
	}
}

func ensureObject(parent map[string]any, key string) map[string]any {
	if current, ok := parent[key].(map[string]any); ok {
		return current
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func stringArray(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []string:
		result = append(result, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	result := make([]string, 0, len(existing)+len(values))
	for _, value := range append(existing, values...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
