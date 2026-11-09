package configmanager

import (
	"encoding/json"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

func TestNormalizeOpenClaw81Contracts(t *testing.T) {
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.8.1")
	content := []byte(`{
  "meta": {"lastTouchedAt": "legacy", "lastTouchedVersion": "2026.7.1-2"},
  "agents": {"defaults": {
    "memorySearch": {"enabled": false, "provider": "local"},
    "models": {"auto/model-b": {}, "auto/model-a": {}},
    "modelPolicy": {"allow": ["existing/model"]},
    "compaction": {"mode": "safeguard", "reserveTokens": 1024, "reserveTokensFloor": 512, "keepRecentTokens": 4096, "maxHistoryShare": 0.5}
  }},
  "browser": {"color": "legacy", "profiles": {"openclaw": {"color": "legacy"}}},
  "commands": {"ownerDisplay": "friendly"},
  "cron": {"enabled": true, "maxConcurrentRuns": 2, "runLog": {"keepLines": 10}},
  "gateway": {
    "controlUi": {"dangerouslyDisableDeviceAuth": true},
    "tailscale": {"mode": "off", "resetOnExit": true},
    "nodes": {"denyCommands": ["legacy.block"], "commands": {"deny": ["new.block"]}},
    "roles": {"worker": true}
  },
  "cloudWorkers": {"profiles": ["unused"]},
  "channels": {"a2a": {"enabled": true}}
}`)
	normalized, changed, err := normalizeConfigMap(content, appconfig.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("8.1 contracts should rewrite retired fields")
	}
	var config map[string]any
	if err := json.Unmarshal(normalized, &config); err != nil {
		t.Fatal(err)
	}
	cron := mustNestedMap(t, config, "cron")
	if _, ok := cron["maxConcurrentRuns"]; ok {
		t.Fatalf("retired cron key remains: %#v", cron)
	}
	if _, ok := cron["runLog"]; ok {
		t.Fatalf("retired cron runLog remains: %#v", cron)
	}
	gatewayConfig := mustNestedMap(t, config, "gateway")
	if _, ok := mustNestedMap(t, gatewayConfig, "controlUi")["dangerouslyDisableDeviceAuth"]; ok {
		t.Fatal("retired Control UI device auth bypass remains")
	}
	commands := mustNestedMap(t, mustNestedMap(t, gatewayConfig, "nodes"), "commands")
	if got := valuesAsSet(commands["deny"]); !got["legacy.block"] || !got["new.block"] {
		t.Fatalf("node deny migration lost values: %#v", commands["deny"])
	}
	if _, ok := config["cloudWorkers"]; ok {
		t.Fatal("cloudWorkers must be disabled by omission")
	}
	if _, ok := gatewayConfig["roles"]; ok {
		t.Fatal("gateway roles must not be enabled")
	}
	if mustNestedMap(t, mustNestedMap(t, config, "tools"), "swarm")["enabled"] != false {
		t.Fatal("native swarm must be disabled")
	}
	if mustNestedMap(t, mustNestedMap(t, mustNestedMap(t, config, "plugins"), "entries"), "a2a")["enabled"] != false {
		t.Fatal("A2A plugin must be disabled")
	}
	if _, ok := mustNestedMap(t, config, "meta")["lastTouchedAt"]; ok {
		t.Fatal("meta.lastTouchedAt must be removed")
	}
	defaults := mustNestedMap(t, mustNestedMap(t, config, "agents"), "defaults")
	if _, ok := defaults["memorySearch"]; ok {
		t.Fatal("agents.defaults.memorySearch must be migrated")
	}
	search := mustNestedMap(t, mustNestedMap(t, config, "memory"), "search")
	if search["enabled"] != false || search["provider"] != "local" {
		t.Fatalf("memory.search lost legacy values: %#v", search)
	}
	if _, ok := defaults["models"]; ok {
		t.Fatal("agents.defaults.models must be migrated")
	}
	allow := valuesAsSet(mustNestedMap(t, defaults, "modelPolicy")["allow"])
	for _, ref := range []string{"existing/model", "auto/model-a", "auto/model-b"} {
		if !allow[ref] {
			t.Fatalf("modelPolicy.allow missing %q: %#v", ref, allow)
		}
	}
	compaction := mustNestedMap(t, defaults, "compaction")
	for _, key := range []string{"reserveTokens", "reserveTokensFloor", "maxHistoryShare"} {
		if _, ok := compaction[key]; ok {
			t.Fatalf("retired compaction key %s remains: %#v", key, compaction)
		}
	}
	if compaction["mode"] != "safeguard" || compaction["keepRecentTokens"] != float64(4096) {
		t.Fatalf("canonical compaction values changed: %#v", compaction)
	}
	if _, ok := mustNestedMap(t, config, "commands")["ownerDisplay"]; ok {
		t.Fatal("commands.ownerDisplay must be removed")
	}
	if _, ok := mustNestedMap(t, gatewayConfig, "tailscale")["resetOnExit"]; ok {
		t.Fatal("gateway.tailscale.resetOnExit must be removed")
	}
	if _, ok := mustNestedMap(t, config, "browser")["color"]; ok {
		t.Fatal("browser.color must be removed")
	}
	if _, ok := mustNestedMap(t, mustNestedMap(t, config, "browser"), "profiles")["openclaw"].(map[string]any)["color"]; ok {
		t.Fatal("browser profile color must be removed")
	}
}

func TestNormalizeOpenClaw71LeavesLegacySchema(t *testing.T) {
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.7.1-2")
	content := []byte(`{"cron":{"maxConcurrentRuns":2},"gateway":{"nodes":{"denyCommands":["legacy.block"]},"controlUi":{"dangerouslyDisableDeviceAuth":true}}}`)
	normalized, _, err := normalizeConfigMap(content, appconfig.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(normalized, &config); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustNestedMap(t, mustNestedMap(t, config, "gateway"), "nodes")["denyCommands"]; !ok {
		t.Fatal("7.1 legacy node deny path was unexpectedly rewritten")
	}
}

func mustNestedMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", key, parent[key])
	}
	return value
}

func valuesAsSet(value any) map[string]bool {
	result := map[string]bool{}
	for _, item := range value.([]any) {
		if text, ok := item.(string); ok {
			result[text] = true
		}
	}
	return result
}
