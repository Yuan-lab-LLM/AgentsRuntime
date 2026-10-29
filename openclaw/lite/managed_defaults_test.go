package openclaw

import "testing"

func assertPlatformDefaults(t *testing.T, config map[string]any) {
	t.Helper()
	models := objectAt(t, config, "models")
	if models["mode"] != "merge" {
		t.Fatalf("models.mode = %#v, want merge", models["mode"])
	}

	agentDefaults := objectAt(t, objectAt(t, config, "agents"), "defaults")
	memorySearch := objectAt(t, agentDefaults, "memorySearch")
	if memorySearch["enabled"] != true || memorySearch["provider"] != "none" {
		t.Fatalf("agents.defaults.memorySearch = %#v, want managed defaults", memorySearch)
	}
	compaction := objectAt(t, agentDefaults, "compaction")
	if compaction["mode"] != "default" ||
		compaction["reserveTokens"] != float64(32768) ||
		compaction["reserveTokensFloor"] != float64(20000) ||
		compaction["keepRecentTokens"] != float64(30000) ||
		compaction["maxHistoryShare"] != 0.65 ||
		compaction["notifyUser"] != true {
		t.Fatalf("agents.defaults.compaction = %#v, want managed defaults", compaction)
	}
	if objectAt(t, compaction, "memoryFlush")["enabled"] != true {
		t.Fatalf("agents.defaults.compaction.memoryFlush = %#v, want enabled", objectAt(t, compaction, "memoryFlush"))
	}
	if agentDefaults["maxConcurrent"] != float64(4) {
		t.Fatalf("agents.defaults.maxConcurrent = %#v, want 4", agentDefaults["maxConcurrent"])
	}
	if objectAt(t, agentDefaults, "subagents")["maxConcurrent"] != float64(8) {
		t.Fatalf("agents.defaults.subagents.maxConcurrent = %#v, want 8", objectAt(t, agentDefaults, "subagents")["maxConcurrent"])
	}

	tools := objectAt(t, config, "tools")
	if tools["profile"] != "full" {
		t.Fatalf("tools.profile = %#v, want full", tools["profile"])
	}
	commands := objectAt(t, config, "commands")
	if commands["native"] != "auto" ||
		commands["nativeSkills"] != "auto" ||
		commands["restart"] != true ||
		commands["ownerDisplay"] != "raw" {
		t.Fatalf("commands = %#v, want managed defaults", commands)
	}
	groupChat := objectAt(t, objectAt(t, config, "messages"), "groupChat")
	if groupChat["visibleReplies"] != "automatic" {
		t.Fatalf("messages.groupChat.visibleReplies = %#v, want automatic", groupChat["visibleReplies"])
	}

	gateway := objectAt(t, config, "gateway")
	if gateway["port"] != float64(20003) {
		t.Fatalf("gateway.port = %#v, want 20003", gateway["port"])
	}
	if gateway["mode"] != "local" {
		t.Fatalf("gateway.mode = %#v, want local", gateway["mode"])
	}
	tailscale := objectAt(t, gateway, "tailscale")
	if tailscale["mode"] != "off" || tailscale["resetOnExit"] != false {
		t.Fatalf("gateway.tailscale = %#v, want managed defaults", tailscale)
	}
	deniedCommands, ok := objectAt(t, gateway, "nodes")["denyCommands"].([]any)
	if !ok {
		t.Fatalf("gateway.nodes.denyCommands = %#v, want array", objectAt(t, gateway, "nodes")["denyCommands"])
	}
	if got := stringSet(deniedCommands); len(got) != len(openClawDefaultDeniedNodeCommands) {
		t.Fatalf("gateway.nodes.denyCommands = %#v, want managed deny list", deniedCommands)
	} else {
		for _, command := range openClawDefaultDeniedNodeCommands {
			if !got[command] {
				t.Fatalf("gateway.nodes.denyCommands missing %q: %#v", command, deniedCommands)
			}
		}
	}

	cron := objectAt(t, config, "cron")
	if cron["enabled"] != true || cron["maxConcurrentRuns"] != float64(2) || cron["sessionRetention"] != "24h" {
		t.Fatalf("cron = %#v, want managed defaults", cron)
	}
	runLog := objectAt(t, cron, "runLog")
	if runLog["keepLines"] != float64(2000) || runLog["maxBytes"] != "2mb" {
		t.Fatalf("cron.runLog = %#v, want managed defaults", runLog)
	}
	update := objectAt(t, config, "update")
	if update["checkOnStart"] != false || objectAt(t, update, "auto")["enabled"] != false {
		t.Fatalf("update = %#v, want managed defaults", update)
	}
	internal := objectAt(t, objectAt(t, config, "hooks"), "internal")
	if internal["enabled"] != true {
		t.Fatalf("hooks.internal.enabled = %#v, want true", internal["enabled"])
	}
	sessionMemory := objectAt(t, objectAt(t, internal, "entries"), "session-memory")
	if sessionMemory["enabled"] != true || sessionMemory["messages"] != float64(50) {
		t.Fatalf("hooks.internal.entries.session-memory = %#v, want managed defaults", sessionMemory)
	}
	session := objectAt(t, config, "session")
	reset := objectAt(t, session, "reset")
	maintenance := objectAt(t, session, "maintenance")
	if reset["idleMinutes"] != float64(10080) || reset["mode"] != "idle" || maintenance["maxEntries"] != float64(2000) || maintenance["pruneAfter"] != "180d" {
		t.Fatalf("session = %#v, want managed defaults", session)
	}

	pluginEntries := objectAt(t, objectAt(t, config, "plugins"), "entries")
	for _, pluginID := range openClawDefaultDisabledPlugins {
		entry := objectAt(t, pluginEntries, pluginID)
		if entry["enabled"] != false {
			t.Fatalf("plugins.entries.%s.enabled = %#v, want false", pluginID, entry["enabled"])
		}
	}
	dreaming := objectAt(t, objectAt(t, objectAt(t, pluginEntries, "memory-core"), "config"), "dreaming")
	if dreaming["enabled"] != true ||
		dreaming["frequency"] != "0 3 * * *" ||
		dreaming["timezone"] != "Asia/Shanghai" {
		t.Fatalf("plugins.entries.memory-core.config.dreaming = %#v, want managed defaults", dreaming)
	}
}
