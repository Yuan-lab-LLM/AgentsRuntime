package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestOpenClaw81ManagedConfigContracts(t *testing.T) {
	config := map[string]any{
		"meta": map[string]any{"lastTouchedAt": "legacy", "lastTouchedVersion": "2026.7.1-2"},
		"agents": map[string]any{"defaults": map[string]any{
			"memorySearch": map[string]any{"enabled": false, "provider": "local"},
			"models":       map[string]any{"auto/model-b": map[string]any{}, "auto/model-a": map[string]any{}},
			"modelPolicy":  map[string]any{"allow": []any{"existing/model"}},
			"compaction": map[string]any{
				"mode": "safeguard", "reserveTokens": 1024, "reserveTokensFloor": 512,
				"keepRecentTokens": 4096, "maxHistoryShare": 0.5,
			},
		}},
		"browser":  map[string]any{"color": "legacy", "profiles": map[string]any{"openclaw": map[string]any{"color": "legacy"}}},
		"commands": map[string]any{"ownerDisplay": "friendly"},
		"cron": map[string]any{
			"enabled":           false,
			"maxConcurrentRuns": 9,
			"runLog":            map[string]any{"keepLines": 4},
		},
		"gateway": map[string]any{
			"tailscale": map[string]any{"mode": "off", "resetOnExit": true},
			"nodes": map[string]any{
				"denyCommands": []any{"custom.legacy"},
				"commands":     map[string]any{"deny": []any{"custom.new"}},
			},
			"roles": map[string]any{"worker": true},
		},
		"cloudWorkers": map[string]any{"profiles": []any{"unused"}},
		"plugins": map[string]any{"entries": map[string]any{
			"acpx":          map[string]any{"enabled": false},
			"phone-control": map[string]any{"enabled": false},
		}},
	}
	mergeOpenClawLiteDefaults(config, "2026.8.1")
	mergePlatformDefaults(config, 20003, "2026.8.1")

	cron := objectAt(t, config, "cron")
	if _, ok := cron["maxConcurrentRuns"]; ok {
		t.Fatalf("8.1 cron retained maxConcurrentRuns: %#v", cron)
	}
	if _, ok := cron["runLog"]; ok {
		t.Fatalf("8.1 cron retained runLog: %#v", cron)
	}
	nodes := objectAt(t, objectAt(t, config, "gateway"), "nodes")
	if _, ok := nodes["denyCommands"]; ok {
		t.Fatalf("8.1 nodes retained denyCommands: %#v", nodes)
	}
	denied := objectAt(t, nodes, "commands")["deny"]
	deniedSet := stringSet(anyStrings(t, denied))
	for _, command := range append([]string{"custom.legacy", "custom.new"}, openClawDefaultDeniedNodeCommands...) {
		if !deniedSet[command] {
			t.Fatalf("8.1 gateway.nodes.commands.deny missing %q: %#v", command, denied)
		}
	}
	if _, ok := config["cloudWorkers"]; ok {
		t.Fatal("managed 8.1 config must disable cloudWorkers by omission")
	}
	if _, ok := objectAt(t, config, "gateway")["roles"]; ok {
		t.Fatal("managed 8.1 config must not opt into gateway roles")
	}
	if objectAt(t, objectAt(t, config, "tools"), "swarm")["enabled"] != false {
		t.Fatal("managed 8.1 config must disable native swarm")
	}
	entries := objectAt(t, objectAt(t, config, "plugins"), "entries")
	for _, retired := range []string{"acpx", "phone-control"} {
		if _, ok := entries[retired]; ok {
			t.Fatalf("managed 8.1 config retained disabled retired plugin %q", retired)
		}
	}
	if _, ok := objectAt(t, config, "meta")["lastTouchedAt"]; ok {
		t.Fatal("managed 8.1 config retained meta.lastTouchedAt")
	}
	agentDefaults := objectAt(t, objectAt(t, config, "agents"), "defaults")
	if _, ok := agentDefaults["memorySearch"]; ok {
		t.Fatal("managed 8.1 config retained agents.defaults.memorySearch")
	}
	search := objectAt(t, objectAt(t, config, "memory"), "search")
	if search["enabled"] != false || search["provider"] != "local" {
		t.Fatalf("managed 8.1 memory.search lost legacy values: %#v", search)
	}
	if _, ok := agentDefaults["models"]; ok {
		t.Fatal("managed 8.1 config retained agents.defaults.models")
	}
	allow := stringSet(anyStrings(t, objectAt(t, agentDefaults, "modelPolicy")["allow"]))
	for _, ref := range []string{"existing/model", "auto/model-a", "auto/model-b"} {
		if !allow[ref] {
			t.Fatalf("managed 8.1 modelPolicy.allow missing %q: %#v", ref, allow)
		}
	}
	compaction := objectAt(t, agentDefaults, "compaction")
	for _, key := range []string{"reserveTokens", "reserveTokensFloor", "maxHistoryShare"} {
		if _, ok := compaction[key]; ok {
			t.Fatalf("managed 8.1 compaction retained %s: %#v", key, compaction)
		}
	}
	if compaction["mode"] != "safeguard" || compaction["keepRecentTokens"] != 4096 {
		t.Fatalf("managed 8.1 compaction lost canonical values: %#v", compaction)
	}
	if _, ok := objectAt(t, config, "commands")["ownerDisplay"]; ok {
		t.Fatal("managed 8.1 config retained commands.ownerDisplay")
	}
	if _, ok := objectAt(t, objectAt(t, config, "gateway"), "tailscale")["resetOnExit"]; ok {
		t.Fatal("managed 8.1 config retained gateway.tailscale.resetOnExit")
	}
	if _, ok := objectAt(t, config, "browser")["color"]; ok {
		t.Fatal("managed 8.1 config retained browser.color")
	}
	if _, ok := objectAt(t, objectAt(t, config, "browser"), "profiles")["openclaw"].(map[string]any)["color"]; ok {
		t.Fatal("managed 8.1 config retained browser profile color")
	}
}

func TestOpenClaw81WriterPreservesLitePathsAndUsesScopedTrustedProxyPairing(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspaces", "openclaw", "user-7", "instance-81")
	req := gateway.CreateGatewayRequest{InstanceID: 81, UserID: 7, AgentType: "openclaw", UID: 0, GID: 0}
	cfg := gateway.Config{GatewayAuthMode: "trusted-proxy", OpenClawVersion: "2026.8.1"}
	if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	if got := objectAt(t, objectAt(t, config, "agents"), "defaults")["workspace"]; got != filepath.ToSlash(filepath.Join(workspace, "home", ".openclaw", "workspace")) {
		t.Fatalf("Lite workspace changed: %#v", got)
	}
	controlUI := objectAt(t, objectAt(t, config, "gateway"), "controlUi")
	if _, ok := controlUI["dangerouslyDisableDeviceAuth"]; ok {
		t.Fatalf("8.1 config retained retired device auth key: %#v", controlUI)
	}
	auth := objectAt(t, objectAt(t, config, "gateway"), "auth")
	trustedProxy := objectAt(t, auth, "trustedProxy")
	deviceAutoApprove := objectAt(t, trustedProxy, "deviceAutoApprove")
	if deviceAutoApprove["enabled"] != true {
		t.Fatalf("8.1 trusted-proxy device auto approval is not enabled: %#v", deviceAutoApprove)
	}
	deviceScopes := stringSet(anyStrings(t, deviceAutoApprove["scopes"]))
	for _, scope := range openClawTrustedProxyDeviceScopes {
		if !deviceScopes[scope] {
			t.Fatalf("8.1 device auto approval missing %q: %#v", scope, deviceScopes)
		}
	}
	if deviceScopes["operator.admin"] || deviceScopes["operator.pairing"] {
		t.Fatalf("8.1 persistent browser device received privileged scope: %#v", deviceScopes)
	}
	identityScopes := objectAt(t, auth, "identityScopes")
	proxyScopes := stringSet(anyStrings(t, identityScopes["/api/v1/instances/81/proxy"]))
	if len(proxyScopes) != 1 || !proxyScopes["operator.admin"] {
		t.Fatalf("8.1 proxy identity scopes = %#v, want operator.admin only", proxyScopes)
	}
}

func TestOpenClaw71WriterKeepsLegacyDeviceAuthWithout81PairingPolicy(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspaces", "openclaw", "user-7", "instance-71")
	req := gateway.CreateGatewayRequest{InstanceID: 71, UserID: 7, AgentType: "openclaw", UID: 0, GID: 0}
	cfg := gateway.Config{GatewayAuthMode: "trusted-proxy", OpenClawVersion: "2026.7.1-2"}
	if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	gatewayConfig := objectAt(t, config, "gateway")
	if objectAt(t, gatewayConfig, "controlUi")["dangerouslyDisableDeviceAuth"] != true {
		t.Fatal("7.1 config lost the legacy trusted-proxy device auth bypass")
	}
	auth := objectAt(t, gatewayConfig, "auth")
	if _, ok := auth["identityScopes"]; ok {
		t.Fatalf("7.1 config contains 8.1 identity scopes: %#v", auth)
	}
	if _, ok := objectAt(t, auth, "trustedProxy")["deviceAutoApprove"]; ok {
		t.Fatalf("7.1 config contains 8.1 device auto approval: %#v", auth)
	}
}

func TestOpenClaw81TokenAuthDoesNotEnableTrustedProxyPairing(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspaces", "openclaw", "user-7", "instance-82")
	req := gateway.CreateGatewayRequest{InstanceID: 82, UserID: 7, AgentType: "openclaw", UID: 0, GID: 0}
	cfg := gateway.Config{GatewayAuthMode: "token", OpenClawVersion: "2026.8.1"}
	if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	auth := objectAt(t, objectAt(t, config, "gateway"), "auth")
	if auth["mode"] != "token" {
		t.Fatalf("8.1 token auth mode = %#v", auth["mode"])
	}
	if _, ok := auth["identityScopes"]; ok {
		t.Fatalf("8.1 token auth contains trusted-proxy identity scopes: %#v", auth)
	}
	if trustedProxy, ok := auth["trustedProxy"].(map[string]any); ok {
		if _, ok := trustedProxy["deviceAutoApprove"]; ok {
			t.Fatalf("8.1 token auth enables trusted-proxy device approval: %#v", trustedProxy)
		}
	}
}

func anyStrings(t *testing.T, value any) []any {
	t.Helper()
	if items, ok := value.([]string); ok {
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want string array", value)
	}
	return items
}
