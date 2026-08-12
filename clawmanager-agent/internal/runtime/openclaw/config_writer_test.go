package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

type Config = gateway.Config
type CreateGatewayRequest = gateway.CreateGatewayRequest

func TestWriteOpenClawGatewayConfigMergesControlUIWithoutOverwritingExistingConfig(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-63")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	existing := []byte(`{
	  "gateway": {
	    "auth": {"mode": "none", "legacy": true, "token": "stale-token"},
	    "controlUi": {
	      "theme": "dark",
	      "allowedOrigins": [
	        "http://localhost:20001",
	        "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"
	      ]
	    },
	    "trustedProxies": ["127.0.0.1"],
	    "keep": "value"
	  },
	  "cron": {"custom": "keep"},
	  "agents": {"defaults": {"model": "auto/gpt-4.1"}}
	}`)
	if err := os.WriteFile(configPath, existing, 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 63, UserID: 45, UID: 200063, GID: 200063}
	cfg := Config{
		GatewayAuthMode: "trusted-proxy",
		PublicOrigin:    "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001",
		AllowedOrigins:  []string{"http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"},
		TrustedProxies:  []string{"127.0.0.1", "10.42.0.0/16"},
		LLMBaseURL:      "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm",
		LLMAPIKey:       "runtime-llm-token",
		LLMAPIKeySet:    true,
		LLMModelIDs:     []string{"gpt-5.5", "gpt-5.5-mini"},
	}
	if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}

	gateway := objectAt(t, merged, "gateway")
	if gateway["port"] != float64(20003) {
		t.Fatalf("gateway.port = %#v, want 20003", gateway["port"])
	}
	auth := objectAt(t, gateway, "auth")
	if auth["mode"] != "trusted-proxy" {
		t.Fatalf("gateway.auth.mode = %#v, want trusted-proxy", auth["mode"])
	}
	if _, ok := auth["token"]; ok {
		t.Fatalf("gateway.auth.token was preserved in trusted-proxy mode")
	}
	if auth["password"] != "9fb3edf4bf38bb834227d41fe9cc1196" {
		t.Fatalf("gateway.auth.password = %#v, want managed trusted-proxy password", auth["password"])
	}
	trustedProxy := objectAt(t, auth, "trustedProxy")
	if trustedProxy["userHeader"] != "x-forwarded-prefix" {
		t.Fatalf("gateway.auth.trustedProxy.userHeader = %#v, want x-forwarded-prefix", trustedProxy["userHeader"])
	}
	allowUsers, ok := trustedProxy["allowUsers"].([]any)
	if !ok {
		t.Fatalf("gateway.auth.trustedProxy.allowUsers = %#v, want array", trustedProxy["allowUsers"])
	}
	if got := stringSet(allowUsers); len(got) != 1 || !got["/api/v1/instances/63/proxy"] {
		t.Fatalf("gateway.auth.trustedProxy.allowUsers = %#v, want instance proxy base path", allowUsers)
	}
	requiredHeaders, ok := trustedProxy["requiredHeaders"].([]any)
	if !ok {
		t.Fatalf("gateway.auth.trustedProxy.requiredHeaders = %#v, want array", trustedProxy["requiredHeaders"])
	}
	if got := stringSet(requiredHeaders); len(got) != 1 || !got["x-forwarded-proto"] {
		t.Fatalf("gateway.auth.trustedProxy.requiredHeaders = %#v, want x-forwarded-proto", requiredHeaders)
	}
	if auth["legacy"] != true {
		t.Fatalf("gateway.auth.legacy was not preserved")
	}
	if gateway["keep"] != "value" {
		t.Fatalf("gateway.keep = %#v, want preserved value", gateway["keep"])
	}
	controlUI := objectAt(t, gateway, "controlUi")
	if controlUI["theme"] != "dark" {
		t.Fatalf("gateway.controlUi.theme = %#v, want preserved value", controlUI["theme"])
	}
	if controlUI["basePath"] != "/api/v1/instances/63/proxy" {
		t.Fatalf("gateway.controlUi.basePath = %#v, want instance proxy base path", controlUI["basePath"])
	}
	if controlUI["dangerouslyDisableDeviceAuth"] != true {
		t.Fatalf("gateway.controlUi.dangerouslyDisableDeviceAuth = %#v, want true for trusted-proxy mode", controlUI["dangerouslyDisableDeviceAuth"])
	}
	origins, ok := controlUI["allowedOrigins"].([]any)
	if !ok {
		t.Fatalf("gateway.controlUi.allowedOrigins = %#v, want array", controlUI["allowedOrigins"])
	}
	if got := stringSet(origins); len(got) != 2 || !got["http://localhost:20001"] || !got["http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"] {
		t.Fatalf("allowedOrigins = %#v, want existing origin plus deduped ClawManager service origin", origins)
	}
	trustedProxies, ok := gateway["trustedProxies"].([]any)
	if !ok {
		t.Fatalf("gateway.trustedProxies = %#v, want array", gateway["trustedProxies"])
	}
	if got := stringSet(trustedProxies); len(got) != 2 || !got["127.0.0.1"] || !got["10.42.0.0/16"] {
		t.Fatalf("trustedProxies = %#v, want existing proxy plus deduped pod-network CIDR", trustedProxies)
	}
	agents := objectAt(t, merged, "agents")
	defaults := objectAt(t, agents, "defaults")
	model := objectAt(t, defaults, "model")
	if model["primary"] != "auto/gpt-5.5" {
		t.Fatalf("agents.defaults.model.primary = %#v, want injected primary model", model["primary"])
	}
	imageModel := objectAt(t, defaults, "imageModel")
	if imageModel["primary"] != "auto/gpt-5.5" {
		t.Fatalf("agents.defaults.imageModel.primary = %#v, want managed primary model", imageModel["primary"])
	}
	agentModels := objectAt(t, defaults, "models")
	if _, ok := agentModels["auto/gpt-5.5"]; !ok {
		t.Fatalf("agents.defaults.models missing auto/gpt-5.5: %#v", agentModels)
	}
	if _, ok := agentModels["auto/gpt-5.5-mini"]; !ok {
		t.Fatalf("agents.defaults.models missing auto/gpt-5.5-mini: %#v", agentModels)
	}
	models := objectAt(t, merged, "models")
	providers := objectAt(t, models, "providers")
	autoProvider := objectAt(t, providers, "auto")
	if autoProvider["baseUrl"] != "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm" {
		t.Fatalf("models.providers.auto.baseUrl = %#v, want injected ClawManager LLM gateway", autoProvider["baseUrl"])
	}
	if autoProvider["apiKey"] != "runtime-llm-token" {
		t.Fatalf("models.providers.auto.apiKey = %#v, want injected runtime token", autoProvider["apiKey"])
	}
	if autoProvider["auth"] != "api-key" {
		t.Fatalf("models.providers.auto.auth = %#v, want api-key", autoProvider["auth"])
	}
	if autoProvider["api"] != "openai-completions" {
		t.Fatalf("models.providers.auto.api = %#v, want openai-completions", autoProvider["api"])
	}
	browser := objectAt(t, merged, "browser")
	if browser["enabled"] != true || browser["headless"] != true || browser["noSandbox"] != true {
		t.Fatalf("browser defaults = %#v, want enabled headless no-sandbox browser", browser)
	}
	if browser["executablePath"] != openClawBrowserExecutablePath {
		t.Fatalf("browser.executablePath = %#v, want %s", browser["executablePath"], openClawBrowserExecutablePath)
	}
	browserPlugin := objectAt(t, objectAt(t, objectAt(t, merged, "plugins"), "entries"), "browser")
	if browserPlugin["enabled"] != true {
		t.Fatalf("plugins.entries.browser.enabled = %#v, want true with enabled browser", browserPlugin["enabled"])
	}
	openclawProfile := objectAt(t, objectAt(t, browser, "profiles"), "openclaw")
	if openclawProfile["driver"] != "openclaw" {
		t.Fatalf("browser.profiles.openclaw.driver = %#v, want openclaw", openclawProfile["driver"])
	}
	if openclawProfile["cdpPort"] != float64(20004) {
		t.Fatalf("browser.profiles.openclaw.cdpPort = %#v, want 20004", openclawProfile["cdpPort"])
	}
	if openclawProfile["color"] != openClawManagedBrowserColor {
		t.Fatalf("browser.profiles.openclaw.color = %#v, want %s", openclawProfile["color"], openClawManagedBrowserColor)
	}
	providerModels, ok := autoProvider["models"].([]any)
	if !ok {
		t.Fatalf("models.providers.auto.models = %#v, want array", autoProvider["models"])
	}
	if got := modelIDSet(providerModels); len(got) != 2 || !got["gpt-5.5"] || !got["gpt-5.5-mini"] {
		t.Fatalf("models.providers.auto.models = %#v, want injected model ids", providerModels)
	}
	assertPlatformDefaults(t, merged)
	if objectAt(t, merged, "cron")["custom"] != "keep" {
		t.Fatalf("cron.custom was not preserved")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 0600", info.Mode().Perm())
		}
	}
}

func TestMergeOpenClawLLMConfigPreservesExplicitImageModel(t *testing.T) {
	config := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"imageModel": map[string]any{"primary": "custom/vision-model"},
			},
		},
	}
	mergeOpenClawLLMConfig(config, Config{
		LLMBaseURL:   "http://clawmanager.test/api/v1/gateway/llm",
		LLMAPIKey:    "managed-token",
		LLMAPIKeySet: true,
		LLMModelIDs:  []string{"auto"},
	})
	defaults := objectAt(t, objectAt(t, config, "agents"), "defaults")
	imageModel := objectAt(t, defaults, "imageModel")
	if imageModel["primary"] != "custom/vision-model" {
		t.Fatalf("agents.defaults.imageModel.primary = %#v, want explicit custom image model preserved", imageModel["primary"])
	}
}

func TestWriteOpenClawGatewayConfigCompletesPartialBrowserConfig(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-64")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"browser":{"enabled":false,"profile":"team-review"}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 64, UserID: 45, UID: 200064, GID: 200064}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	merged := readOpenClawConfigForTest(t, configPath)
	browser := objectAt(t, merged, "browser")
	if browser["enabled"] != false {
		t.Fatalf("browser.enabled = %#v, want explicit false preserved", browser["enabled"])
	}
	browserPlugin := objectAt(t, objectAt(t, objectAt(t, merged, "plugins"), "entries"), "browser")
	if browserPlugin["enabled"] != false {
		t.Fatalf("plugins.entries.browser.enabled = %#v, want false with disabled browser", browserPlugin["enabled"])
	}
	if browser["profile"] != "team-review" {
		t.Fatalf("browser.profile = %#v, want preserved custom profile", browser["profile"])
	}
	if browser["executablePath"] != openClawBrowserExecutablePath || browser["headless"] != true || browser["noSandbox"] != true {
		t.Fatalf("completed browser config = %#v", browser)
	}
	openclawProfile := objectAt(t, objectAt(t, browser, "profiles"), "openclaw")
	if openclawProfile["cdpPort"] != float64(20004) {
		t.Fatalf("browser.profiles.openclaw.cdpPort = %#v, want 20004", openclawProfile["cdpPort"])
	}
}

func TestWriteOpenClawGatewayConfigPreservesExplicitTrustedProxyPassword(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-651")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"gateway":{"auth":{"password":"custom-password"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 651, UserID: 45, UID: 200651, GID: 200651}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	auth := objectAt(t, objectAt(t, readOpenClawConfigForTest(t, configPath), "gateway"), "auth")
	if auth["password"] != "custom-password" {
		t.Fatalf("gateway.auth.password = %#v, want explicit password preserved", auth["password"])
	}
}

func TestWriteOpenClawGatewayConfigPreservesExplicitBrowserConfig(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-65")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	existing := []byte(`{"browser":{"enabled":false,"executablePath":"/opt/custom-browser","headless":false,"noSandbox":false}}`)
	if err := os.WriteFile(configPath, existing, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 65, UserID: 45, UID: 200065, GID: 200065}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	merged := readOpenClawConfigForTest(t, configPath)
	browser := objectAt(t, merged, "browser")
	if browser["enabled"] != false || browser["executablePath"] != "/opt/custom-browser" || browser["headless"] != false || browser["noSandbox"] != false {
		t.Fatalf("explicit browser config was overwritten: %#v", browser)
	}
	browserPlugin := objectAt(t, objectAt(t, objectAt(t, merged, "plugins"), "entries"), "browser")
	if browserPlugin["enabled"] != false {
		t.Fatalf("plugins.entries.browser.enabled = %#v, want false with explicit browser disable", browserPlugin["enabled"])
	}
}

func TestWriteOpenClawGatewayConfigForcesTeamBrowserThroughManagedProxy(t *testing.T) {
	existing := []byte(`{
	  "browser": {
	    "enabled": false,
	    "extraArgs": [
	      "--window-size=1280,720",
	      "--no-proxy-server",
	      "--proxy-server=http://untrusted.example:8080",
	      "--proxy-bypass-list=*",
	      "--proxy-pac-url=http://untrusted.example/proxy.pac"
	    ],
	    "ssrfPolicy": {
	      "allowedHostnames": ["example.com"],
	      "hostnameAllowlist": ["*.customer.example"]
	    }
	  }
	}`)
	var config map[string]any
	if err := json.Unmarshal(existing, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	proxyURL := "http://clawmanager-egress-proxy.clawmanager-system.svc.cluster.local:3128"
	req := CreateGatewayRequest{
		InstanceID: 66,
		UserID:     45,
		UID:        200066,
		GID:        200066,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":      "true",
			"CLAWMANAGER_BROWSER_PROXY_URL": proxyURL,
		},
	}
	configureManagedOpenClawBrowser(config, req, 20003)

	browser := objectAt(t, config, "browser")
	if browser["enabled"] != true {
		t.Fatalf("browser.enabled = %#v, want true for Team worker", browser["enabled"])
	}
	args, ok := browser["extraArgs"].([]string)
	if !ok {
		t.Fatalf("browser.extraArgs = %#v, want string array", browser["extraArgs"])
	}
	gotArgs := map[string]bool{}
	for _, argument := range args {
		gotArgs[argument] = true
	}
	for _, required := range []string{
		"--window-size=1280,720",
		"--proxy-server=" + proxyURL,
		"--proxy-bypass-list=<-loopback>",
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
	} {
		if !gotArgs[required] {
			t.Fatalf("browser.extraArgs missing %q: %#v", required, args)
		}
	}
	for _, forbidden := range []string{
		"--no-proxy-server",
		"--proxy-server=http://untrusted.example:8080",
		"--proxy-bypass-list=*",
		"--proxy-pac-url=http://untrusted.example/proxy.pac",
	} {
		if gotArgs[forbidden] {
			t.Fatalf("browser.extraArgs retained conflicting argument %q: %#v", forbidden, args)
		}
	}
	ssrfPolicy := objectAt(t, browser, "ssrfPolicy")
	if ssrfPolicy["dangerouslyAllowPrivateNetwork"] != true {
		t.Fatalf("browser.ssrfPolicy = %#v, want managed proxy allowance", ssrfPolicy)
	}
	allowedHostnames, ok := ssrfPolicy["allowedHostnames"].([]any)
	if !ok || !stringSet(allowedHostnames)["example.com"] {
		t.Fatalf("browser.ssrfPolicy.allowedHostnames was not preserved: %#v", ssrfPolicy)
	}
	hostnameAllowlist, ok := ssrfPolicy["hostnameAllowlist"].([]any)
	if !ok || len(hostnameAllowlist) != 1 || hostnameAllowlist[0] != "*.customer.example" {
		t.Fatalf("browser.ssrfPolicy.hostnameAllowlist was widened or replaced: %#v", ssrfPolicy)
	}
}

func TestManagedTeamBrowserDoesNotCreateExclusiveHostnameAllowlist(t *testing.T) {
	proxyURL := "http://clawmanager-egress-proxy.clawmanager-system.svc.cluster.local:3128"
	req := CreateGatewayRequest{
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":      "true",
			"CLAWMANAGER_BROWSER_PROXY_URL": proxyURL,
		},
	}
	config := map[string]any{}
	configureManagedOpenClawBrowser(config, req, 20003)
	ssrfPolicy := objectAt(t, objectAt(t, config, "browser"), "ssrfPolicy")
	if _, exists := ssrfPolicy["hostnameAllowlist"]; exists {
		t.Fatalf("managed Browser introduced an exclusive hostname allowlist: %#v", ssrfPolicy)
	}
}

func TestWriteOpenClawGatewayConfigDoesNotRelaxSSRFWithoutManagedTeamProxy(t *testing.T) {
	req := CreateGatewayRequest{
		InstanceID: 67,
		UserID:     45,
		UID:        200067,
		GID:        200067,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED": "true",
		},
	}
	config := map[string]any{}
	configureManagedOpenClawBrowser(config, req, 20003)
	browser := objectAt(t, config, "browser")
	if _, exists := browser["ssrfPolicy"]; exists {
		t.Fatalf("browser.ssrfPolicy was relaxed without a managed proxy: %#v", browser)
	}
}

func TestManagedTeamBrowserUsesExistingClawManagerHTTPSProxyForUpgrades(t *testing.T) {
	proxyURL := "http://clawmanager-egress-proxy.clawmanager-system.svc.cluster.local:3128"
	req := CreateGatewayRequest{
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED": "true",
			"HTTPS_PROXY":              proxyURL,
		},
	}
	config := map[string]any{}
	configureManagedOpenClawBrowser(config, req, 20003)
	browser := objectAt(t, config, "browser")
	if _, exists := browser["ssrfPolicy"]; !exists {
		t.Fatalf("existing Team instance did not adopt managed Browser proxy: %#v", browser)
	}
	args, ok := browser["extraArgs"].([]string)
	if !ok {
		t.Fatalf("browser.extraArgs = %#v, want string array", browser["extraArgs"])
	}
	found := false
	for _, argument := range args {
		if argument == "--proxy-server="+proxyURL {
			found = true
		}
	}
	if !found {
		t.Fatalf("browser.extraArgs missing managed proxy: %#v", args)
	}
}

func TestManagedTeamBrowserRejectsUntrustedGenericProxyFallback(t *testing.T) {
	req := CreateGatewayRequest{
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED": "true",
			"HTTPS_PROXY":              "http://untrusted.example:3128",
		},
	}
	config := map[string]any{}
	configureManagedOpenClawBrowser(config, req, 20003)
	browser := objectAt(t, config, "browser")
	if _, exists := browser["ssrfPolicy"]; exists {
		t.Fatalf("untrusted proxy fallback relaxed Browser SSRF: %#v", browser)
	}
}

func TestWriteOpenClawGatewayConfigPinsManagedBrowserCDPInsideAllocatedPortBlock(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-415")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	existing := []byte(`{
  "browser": {
    "enabled": false,
    "executablePath": "/opt/custom-browser",
    "headless": false,
    "noSandbox": false,
    "profiles": {
      "openclaw": {
        "driver": "legacy",
        "cdpPort": 20053,
        "color": "#00AA00",
        "viewport": "keep"
      },
      "remote-review": {
        "driver": "remote",
        "cdpPort": 41000
      }
    }
  }
}`)
	if err := os.WriteFile(configPath, existing, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 415, UserID: 45, UID: 200415, GID: 200415}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20042); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "20053") {
		t.Fatalf("config still contains stale default CDP port 20053: %s", string(data))
	}
	merged := readOpenClawConfigForTest(t, configPath)
	gatewayConfig := objectAt(t, merged, "gateway")
	if gatewayConfig["port"] != float64(20042) {
		t.Fatalf("gateway.port = %#v, want 20042", gatewayConfig["port"])
	}
	browser := objectAt(t, merged, "browser")
	if browser["enabled"] != false || browser["executablePath"] != "/opt/custom-browser" || browser["headless"] != false || browser["noSandbox"] != false {
		t.Fatalf("explicit browser config was overwritten: %#v", browser)
	}
	profiles := objectAt(t, browser, "profiles")
	openclawProfile := objectAt(t, profiles, "openclaw")
	if openclawProfile["driver"] != "openclaw" {
		t.Fatalf("browser.profiles.openclaw.driver = %#v, want openclaw", openclawProfile["driver"])
	}
	if openclawProfile["cdpPort"] != float64(20043) {
		t.Fatalf("browser.profiles.openclaw.cdpPort = %#v, want 20043", openclawProfile["cdpPort"])
	}
	if openclawProfile["color"] != "#00AA00" || openclawProfile["viewport"] != "keep" {
		t.Fatalf("openclaw profile non-port config was not preserved: %#v", openclawProfile)
	}
	remoteProfile := objectAt(t, profiles, "remote-review")
	if remoteProfile["driver"] != "remote" || remoteProfile["cdpPort"] != float64(41000) {
		t.Fatalf("remote browser profile was not preserved: %#v", remoteProfile)
	}
	if gotBrowserControlPort := int(openclawProfile["cdpPort"].(float64)) + 1; gotBrowserControlPort != 20044 {
		t.Fatalf("derived Browser Control port = %d, want 20044", gotBrowserControlPort)
	}
}

func TestWriteOpenClawGatewayConfigRejectsPortBlockOutsideValidRange(t *testing.T) {
	for _, port := range []int{0, 65534} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-67")
			req := CreateGatewayRequest{InstanceID: 67, UserID: 45, UID: 200067, GID: 200067}

			err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, port)
			if err == nil {
				t.Fatal("WriteGatewayConfig() error = nil, want invalid port error")
			}
			if !strings.Contains(err.Error(), "invalid gateway port") {
				t.Fatalf("WriteGatewayConfig() error = %q, want invalid gateway port", err)
			}
		})
	}
}

func TestWriteOpenClawGatewayConfigPreservesExplicitLiteDefaults(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-66")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	existing := []byte(`{
  "models": {"mode": "replace"},
  "agents": {
    "defaults": {
      "memorySearch": {"enabled": false, "provider": "local"},
      "compaction": {
        "mode": "safeguard",
        "reserveTokens": 1024,
        "reserveTokensFloor": 512,
        "keepRecentTokens": 4096,
        "maxHistoryShare": 0.5,
        "notifyUser": false,
        "memoryFlush": {"enabled": false}
      },
      "maxConcurrent": 2,
      "subagents": {"maxConcurrent": 3}
    }
  },
  "tools": {"profile": "minimal"},
  "commands": {
    "native": "off",
    "nativeSkills": "off",
    "restart": false,
    "ownerDisplay": "friendly"
  },
  "messages": {"groupChat": {"visibleReplies": "message_tool_only"}},
  "gateway": {
    "mode": "remote",
    "tailscale": {"mode": "on", "resetOnExit": true},
    "nodes": {"denyCommands": ["custom.block"]}
  },
  "plugins": {
    "entries": {
      "bonjour": {"enabled": true},
      "memory-core": {
        "config": {
          "dreaming": {
            "enabled": false,
            "frequency": "custom",
            "timezone": "UTC"
          }
        }
      }
    }
  }
}`)
	if err := os.WriteFile(configPath, existing, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	req := CreateGatewayRequest{InstanceID: 66, UserID: 45, UID: 200066, GID: 200066}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	merged := readOpenClawConfigForTest(t, configPath)
	if got := objectAt(t, merged, "models")["mode"]; got != "replace" {
		t.Fatalf("models.mode = %#v, want explicit replace preserved", got)
	}
	agentDefaults := objectAt(t, objectAt(t, merged, "agents"), "defaults")
	memorySearch := objectAt(t, agentDefaults, "memorySearch")
	if memorySearch["enabled"] != false || memorySearch["provider"] != "local" {
		t.Fatalf("explicit memorySearch was overwritten: %#v", memorySearch)
	}
	compaction := objectAt(t, agentDefaults, "compaction")
	if compaction["mode"] != "safeguard" ||
		compaction["reserveTokens"] != float64(1024) ||
		compaction["reserveTokensFloor"] != float64(512) ||
		compaction["keepRecentTokens"] != float64(4096) ||
		compaction["maxHistoryShare"] != 0.5 ||
		compaction["notifyUser"] != false ||
		objectAt(t, compaction, "memoryFlush")["enabled"] != false {
		t.Fatalf("explicit compaction was overwritten: %#v", compaction)
	}
	if agentDefaults["maxConcurrent"] != float64(2) || objectAt(t, agentDefaults, "subagents")["maxConcurrent"] != float64(3) {
		t.Fatalf("explicit agent concurrency was overwritten: %#v", agentDefaults)
	}
	if objectAt(t, merged, "tools")["profile"] != "minimal" {
		t.Fatalf("explicit tools.profile was overwritten: %#v", objectAt(t, merged, "tools"))
	}
	commands := objectAt(t, merged, "commands")
	if commands["native"] != "off" ||
		commands["nativeSkills"] != "off" ||
		commands["restart"] != false ||
		commands["ownerDisplay"] != "friendly" {
		t.Fatalf("explicit commands were overwritten: %#v", commands)
	}
	if objectAt(t, objectAt(t, merged, "messages"), "groupChat")["visibleReplies"] != "message_tool_only" {
		t.Fatalf("explicit visibleReplies was overwritten")
	}
	gateway := objectAt(t, merged, "gateway")
	if gateway["mode"] != "remote" {
		t.Fatalf("gateway.mode = %#v, want explicit remote preserved", gateway["mode"])
	}
	tailscale := objectAt(t, gateway, "tailscale")
	if tailscale["mode"] != "on" || tailscale["resetOnExit"] != true {
		t.Fatalf("explicit gateway.tailscale was overwritten: %#v", tailscale)
	}
	deniedCommands, ok := objectAt(t, gateway, "nodes")["denyCommands"].([]any)
	if !ok {
		t.Fatalf("gateway.nodes.denyCommands = %#v, want array", objectAt(t, gateway, "nodes")["denyCommands"])
	}
	deniedSet := stringSet(deniedCommands)
	if len(deniedSet) != len(openClawDefaultDeniedNodeCommands)+1 || !deniedSet["custom.block"] {
		t.Fatalf("gateway.nodes.denyCommands = %#v, want custom command plus managed deny list", deniedCommands)
	}
	if objectAt(t, objectAt(t, objectAt(t, merged, "plugins"), "entries"), "bonjour")["enabled"] != true {
		t.Fatalf("explicit bonjour enabled state was overwritten")
	}
	dreaming := objectAt(t, objectAt(t, objectAt(t, objectAt(t, objectAt(t, merged, "plugins"), "entries"), "memory-core"), "config"), "dreaming")
	if dreaming["enabled"] != false || dreaming["frequency"] != "custom" || dreaming["timezone"] != "UTC" {
		t.Fatalf("explicit memory-core dreaming config was overwritten: %#v", dreaming)
	}
}

func readOpenClawConfigForTest(t *testing.T, configPath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return config
}

func TestWriteOpenClawGatewayConfigUsesRequestEnvironmentLLMOverrides(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-68")
	req := CreateGatewayRequest{
		InstanceID: 68,
		UserID:     45,
		UID:        200068,
		GID:        200068,
		Environment: map[string]string{
			"CLAWMANAGER_LLM_BASE_URL":          "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm",
			"CLAWMANAGER_LLM_API_KEY":           "instance-token",
			"CLAWMANAGER_LLM_MODEL":             `["auto","gpt-5.5"]`,
			"CLAWMANAGER_LLM_REASONING":         `{"auto":false,"gpt-5.5":true}`,
			"CLAWMANAGER_LLM_REASONING_CONTROL": `{"auto":"","gpt-5.5":"deepseek-thinking"}`,
		},
	}
	cfg := Config{GatewayAuthMode: "trusted-proxy"}

	if err := WriteGatewayConfig(cfg, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	autoProvider := objectAt(t, objectAt(t, objectAt(t, merged, "models"), "providers"), "auto")
	if autoProvider["apiKey"] != "instance-token" {
		t.Fatalf("models.providers.auto.apiKey = %#v, want request token", autoProvider["apiKey"])
	}
	providerModels, ok := autoProvider["models"].([]any)
	if !ok || len(providerModels) != 2 {
		t.Fatalf("models.providers.auto.models = %#v, want two managed models", autoProvider["models"])
	}
	if providerModels[0].(map[string]any)["reasoning"] != false || providerModels[1].(map[string]any)["reasoning"] != true {
		t.Fatalf("managed reasoning settings were not applied: %#v", providerModels)
	}
	reasoningCompat, ok := providerModels[1].(map[string]any)["compat"].(map[string]any)
	if !ok || reasoningCompat["supportsReasoningEffort"] != true {
		t.Fatalf("managed reasoning control compat was not applied: %#v", providerModels[1])
	}
	effortMap, ok := reasoningCompat["reasoningEffortMap"].(map[string]any)
	if !ok || effortMap["off"] != "none" || effortMap["medium"] != "high" || effortMap["max"] != "max" {
		t.Fatalf("managed reasoning effort map is incomplete: %#v", effortMap)
	}
	defaults := objectAt(t, objectAt(t, merged, "agents"), "defaults")
	model := objectAt(t, defaults, "model")
	if model["primary"] != "auto/auto" {
		t.Fatalf("agents.defaults.model.primary = %#v, want first request model", model["primary"])
	}
}

func TestWriteOpenClawGatewayConfigMergesRequestChannelsIntoWorkspaceConfig(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-70")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "channels": {
    "discord": {"enabled": true},
    "telegram": {"enabled": false}
  }
}`), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	req := CreateGatewayRequest{
		InstanceID: 70,
		UserID:     45,
		UID:        200070,
		GID:        200070,
		Environment: map[string]string{
			"CLAWMANAGER_OPENCLAW_CHANNELS_JSON": `{"telegram":{"enabled":true},"feishu":{"enabled":true}}`,
		},
	}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	channels := objectAt(t, merged, "channels")
	if objectAt(t, channels, "telegram")["enabled"] != true {
		t.Fatalf("channels.telegram.enabled = %#v, want true", objectAt(t, channels, "telegram")["enabled"])
	}
	if objectAt(t, channels, "feishu")["enabled"] != true {
		t.Fatalf("channels.feishu.enabled = %#v, want true", objectAt(t, channels, "feishu")["enabled"])
	}
	if objectAt(t, channels, "discord")["enabled"] != true {
		t.Fatalf("channels.discord.enabled = %#v, want preserved true", objectAt(t, channels, "discord")["enabled"])
	}
}

func TestWriteOpenClawGatewayConfigReconcilesManagedChannelPluginEntries(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-72")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "plugins": {
    "entries": {
      "dingtalk-connector": {"enabled": false, "custom": "keep"},
      "feishu": {"enabled": false},
      "wecom-openclaw-plugin": {"enabled": true}
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	req := CreateGatewayRequest{
		InstanceID: 72,
		UserID:     45,
		UID:        200072,
		GID:        200072,
		Environment: map[string]string{
			"CLAWMANAGER_OPENCLAW_CHANNELS_JSON": `{"dingtalk":{},"feishu":{}}`,
		},
	}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	entries := objectAt(t, objectAt(t, merged, "plugins"), "entries")
	if got := objectAt(t, entries, "dingtalk-connector")["enabled"]; got != true {
		t.Fatalf("plugins.entries.dingtalk-connector.enabled = %#v, want true", got)
	}
	if got := objectAt(t, entries, "feishu")["enabled"]; got != true {
		t.Fatalf("plugins.entries.feishu.enabled = %#v, want true", got)
	}
	if got := objectAt(t, entries, "wecom-openclaw-plugin")["enabled"]; got != false {
		t.Fatalf("plugins.entries.wecom-openclaw-plugin.enabled = %#v, want false", got)
	}
	if got := objectAt(t, entries, "dingtalk-connector")["custom"]; got != "keep" {
		t.Fatalf("plugins.entries.dingtalk-connector.custom = %#v, want preserved value", got)
	}
	groupChat := objectAt(t, objectAt(t, merged, "messages"), "groupChat")
	if got := groupChat["visibleReplies"]; got != "automatic" {
		t.Fatalf("messages.groupChat.visibleReplies = %#v, want automatic", got)
	}
}

func TestWriteOpenClawGatewayConfigPreservesExplicitDingTalkVisibleReplies(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-73")
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "messages": {
    "groupChat": {
      "visibleReplies": "message_tool_only"
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	req := CreateGatewayRequest{
		InstanceID: 73,
		UserID:     45,
		Environment: map[string]string{
			"CLAWMANAGER_OPENCLAW_CHANNELS_JSON": `{"dingtalk-connector":{}}`,
		},
	}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	groupChat := objectAt(t, objectAt(t, merged, "messages"), "groupChat")
	if got := groupChat["visibleReplies"]; got != "message_tool_only" {
		t.Fatalf("messages.groupChat.visibleReplies = %#v, want preserved explicit value", got)
	}
}

func TestMergeOpenClawChannelsUsesEnvFallbackAndEnvironmentPriority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment map[string]string
		env         map[string]string
		wantEnabled bool
	}{
		{
			name: "Env fallback",
			env: map[string]string{
				openClawChannelsEnv: `{"telegram":{"enabled":false}}`,
			},
			wantEnabled: false,
		},
		{
			name: "Environment priority",
			environment: map[string]string{
				openClawChannelsEnv: `{"telegram":{"enabled":true}}`,
			},
			env: map[string]string{
				openClawChannelsEnv: `{"telegram":{"enabled":false}}`,
			},
			wantEnabled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{}
			req := CreateGatewayRequest{Environment: tc.environment, Env: tc.env}
			if err := mergeOpenClawChannelsFromRequest(config, req); err != nil {
				t.Fatalf("mergeOpenClawChannelsFromRequest() error = %v", err)
			}
			telegram := objectAt(t, objectAt(t, config, "channels"), "telegram")
			if got := telegram["enabled"]; got != tc.wantEnabled {
				t.Fatalf("channels.telegram.enabled = %#v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestWriteOpenClawGatewayConfigRejectsInvalidRequestChannelsPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{name: "array", payload: `[]`},
		{name: "invalid json", payload: `{"telegram":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-71")
			req := CreateGatewayRequest{
				InstanceID: 71,
				UserID:     45,
				UID:        200071,
				GID:        200071,
				Environment: map[string]string{
					"CLAWMANAGER_OPENCLAW_CHANNELS_JSON": tc.payload,
				},
			}
			err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003)
			if err == nil {
				t.Fatal("WriteGatewayConfig() error = nil, want invalid channels payload error")
			}
			if !strings.Contains(err.Error(), "CLAWMANAGER_OPENCLAW_CHANNELS_JSON") {
				t.Fatalf("WriteGatewayConfig() error = %q, want environment name", err)
			}
		})
	}
}

func TestWriteOpenClawGatewayConfigWritesLiteTeamConfigJSON(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-45", "instance-69")
	req := CreateGatewayRequest{
		AgentType:  "openclaw",
		InstanceID: 69,
		UserID:     45,
		UID:        200069,
		GID:        200069,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_CONFIG_JSON": `{"teamId":"team-1","members":[{"memberId":"leader"}]}`,
			"CLAWMANAGER_TEAM_SHARED_DIR":  "/team",
		},
	}

	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "team", "team.json"))
	if err != nil {
		t.Fatalf("read lite team config: %v", err)
	}
	var teamConfig map[string]any
	if err := json.Unmarshal(data, &teamConfig); err != nil {
		t.Fatalf("parse lite team config: %v", err)
	}
	if teamConfig["teamId"] != "team-1" {
		t.Fatalf("teamId = %#v, want team-1", teamConfig["teamId"])
	}
}

func TestWriteOpenClawGatewayConfigEnablesRedisTeamForLiteTeam(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-106")
	sourcePlugin := filepath.Join(root, "defaults", ".openclaw", "extensions", "redis-team")
	if err := os.MkdirAll(filepath.Join(sourcePlugin, "dist"), 0o755); err != nil {
		t.Fatalf("mkdir source plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcePlugin, "openclaw.plugin.json"), []byte(`{"id":"redis-team","channels":["redis-team"]}`), 0o644); err != nil {
		t.Fatalf("write source manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcePlugin, "package.json"), []byte(`{"name":"@clawmanager/openclaw-redis-team","version":"0.2.1"}`), 0o644); err != nil {
		t.Fatalf("write source package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcePlugin, "dist", "index.js"), []byte(`module.exports = {};`), 0o644); err != nil {
		t.Fatalf("write source plugin entrypoint: %v", err)
	}
	t.Setenv("CLAWMANAGER_OPENCLAW_REDIS_TEAM_PLUGIN_DIR", sourcePlugin)

	req := CreateGatewayRequest{
		AgentType:  "openclaw",
		InstanceID: 106,
		UserID:     1,
		UID:        200106,
		GID:        200106,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":        "true",
			"CLAWMANAGER_TEAM_AUTORUN":        "true",
			"CLAWMANAGER_TEAM_REDIS_URL":      "redis://clawmanager-team-redis:6379/0",
			"CLAWMANAGER_TEAM_ID":             "26",
			"CLAWMANAGER_TEAM_MEMBER_ID":      "leader",
			"CLAWMANAGER_TEAM_ROLE":           "leader",
			"CLAWMANAGER_TEAM_INBOX_KEY":      "claw:team:26:inbox:leader",
			"CLAWMANAGER_TEAM_EVENTS_KEY":     "claw:team:26:events",
			"CLAWMANAGER_TEAM_PRESENCE_KEY":   "claw:team:26:presence",
			"CLAWMANAGER_TEAM_DLQ_KEY":        "claw:team:26:dlq:leader",
			"CLAWMANAGER_TEAM_CONSUMER_GROUP": "team-members",
			"CLAWMANAGER_TEAM_SHARED_DIR":     "/team",
		},
	}

	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20003); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	pluginEntry := objectAt(t, objectAt(t, objectAt(t, cfg, "plugins"), "entries"), "redis-team")
	if got := pluginEntry["enabled"]; got != true {
		t.Fatalf("plugins.entries.redis-team.enabled = %#v, want true", got)
	}
	account := objectAt(t, objectAt(t, objectAt(t, objectAt(t, cfg, "channels"), "redis-team"), "accounts"), "default")
	if got := account["fromEnv"]; got != true {
		t.Fatalf("channels.redis-team.accounts.default.fromEnv = %#v, want true", got)
	}
	if got := account["enabled"]; got != true {
		t.Fatalf("channels.redis-team.accounts.default.enabled = %#v, want true", got)
	}
	if got := account["autoRun"]; got != true {
		t.Fatalf("channels.redis-team.accounts.default.autoRun = %#v, want true", got)
	}
	if got := account["redisUrl"]; got != "redis://clawmanager-team-redis:6379/0" {
		t.Fatalf("channels.redis-team.accounts.default.redisUrl = %#v, want request redis url", got)
	}
	if got := account["memberId"]; got != "leader" {
		t.Fatalf("channels.redis-team.accounts.default.memberId = %#v, want leader", got)
	}
	if got := account["inboxKey"]; got != "claw:team:26:inbox:leader" {
		t.Fatalf("channels.redis-team.accounts.default.inboxKey = %#v, want leader inbox key", got)
	}
	if got := account["eventsKey"]; got != "claw:team:26:events" {
		t.Fatalf("channels.redis-team.accounts.default.eventsKey = %#v, want team events key", got)
	}
	if got := account["presenceKey"]; got != "claw:team:26:presence" {
		t.Fatalf("channels.redis-team.accounts.default.presenceKey = %#v, want team presence key", got)
	}
	if got := account["consumerGroup"]; got != "team-members" {
		t.Fatalf("channels.redis-team.accounts.default.consumerGroup = %#v, want team-members", got)
	}
	if got := account["sharedDir"]; got != filepath.Join(workspace, "team") {
		t.Fatalf("channels.redis-team.accounts.default.sharedDir = %#v, want remapped workspace team dir", got)
	}

	copiedManifest := filepath.Join(workspace, "home", ".openclaw", "extensions", "redis-team", "openclaw.plugin.json")
	if _, err := os.Stat(copiedManifest); err != nil {
		t.Fatalf("expected redis-team plugin manifest to be seeded at %s: %v", copiedManifest, err)
	}
}

func TestSeedOpenClawRedisTeamPluginUpdatesOnlyRecognizedManagedCopy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "defaults", "redis-team")
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(workspace, "home", ".openclaw", "extensions", "redis-team")
	writeManagedRedisTeamPlugin(t, source, "0.2.1", "new-runtime")
	writeManagedRedisTeamPlugin(t, target, "0.2.1", "old-runtime")
	t.Setenv(openClawRedisTeamPluginDirEnv, source)
	if err := seedOpenClawRedisTeamPlugin(gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "dist", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-runtime" {
		t.Fatalf("managed redis-team plugin was not updated: %q", data)
	}
	if _, err := os.Stat(filepath.Join(workspace, "home", ".openclaw", "extensions", "another-plugin")); !os.IsNotExist(err) {
		t.Fatalf("redis-team synchronization must not create or touch other plugins: %v", err)
	}
}

func TestSeedOpenClawRedisTeamPluginPreservesUnrecognizedExtension(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "defaults", "redis-team")
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(workspace, "home", ".openclaw", "extensions", "redis-team")
	writeManagedRedisTeamPlugin(t, source, "0.2.1", "managed-runtime")
	if err := os.MkdirAll(filepath.Join(target, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "package.json"), []byte(`{"name":"user-owned-plugin","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "openclaw.plugin.json"), []byte(`{"id":"redis-team","channels":["redis-team"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "dist", "index.js"), []byte("user-runtime"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(openClawRedisTeamPluginDirEnv, source)
	if err := seedOpenClawRedisTeamPlugin(gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "dist", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "user-runtime" {
		t.Fatalf("unrecognized user extension was overwritten: %q", data)
	}
}

func writeManagedRedisTeamPlugin(t *testing.T, root, version, runtime string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@clawmanager/openclaw-redis-team","version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "openclaw.plugin.json"), []byte(`{"id":"redis-team","channels":["redis-team"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.js"), []byte(runtime), 0o644); err != nil {
		t.Fatal(err)
	}
}

func objectAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", key, parent[key])
	}
	return value
}

func stringSet(values []any) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		if text, ok := value.(string); ok {
			out[text] = true
		}
	}
	return out
}

func modelIDSet(values []any) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		model, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, ok := model["id"].(string)
		if ok {
			out[id] = true
		}
	}
	return out
}
