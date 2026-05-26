package configmanager

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

func TestNormalizeActiveConfigSupportsGatewayModelList(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWMANAGER_LLM_MODEL", `["auto","gpt-4.1","claude-3.7-sonnet","deepseek-r1"]`)
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "https://gateway.example/v1")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "")

	bundledDir := t.TempDir()
	userDir := t.TempDir()
	manager := New(appconfig.Config{
		OpenClawConfigPath:           configPath,
		OpenClawBundledExtensionsDir: bundledDir,
		OpenClawExtensionsDir:        userDir,
		InstalledPluginPathPrefix:    "/does-not-exist/",
	}, nil, nil)
	if err := manager.NormalizeActiveConfig(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	provider := nestedMapForTest(t, cfg, "models", "providers", "auto")

	if got := provider["baseUrl"]; got != "https://gateway.example/v1" {
		t.Fatalf("expected gateway baseUrl override, got %#v", got)
	}
	if got := provider["apiKey"]; got != "" {
		t.Fatalf("expected empty apiKey override, got %#v", got)
	}
	if _, ok := provider["auth"]; ok {
		t.Fatalf("did not expect auth override for empty apiKey, got %#v", provider["auth"])
	}

	expectedModels := []string{"auto", "gpt-4.1", "claude-3.7-sonnet", "deepseek-r1"}
	if got := providerModelIDsForTest(t, provider); !equalStringSlices(got, expectedModels) {
		t.Fatalf("unexpected provider models: got %v want %v", got, expectedModels)
	}

	defaults := nestedMapForTest(t, cfg, "agents", "defaults")
	model := nestedMapForTest(t, defaults, "model")
	if got := model["primary"]; got != "auto/auto" {
		t.Fatalf("expected primary auto/auto, got %#v", got)
	}

	gotKeys := mapKeysForTest(t, defaults["models"])
	expectedKeys := []string{
		"auto/auto",
		"auto/claude-3.7-sonnet",
		"auto/deepseek-r1",
		"auto/gpt-4.1",
	}
	sort.Strings(expectedKeys)
	if !equalStringSlices(gotKeys, expectedKeys) {
		t.Fatalf("unexpected agent models keys: got %v want %v", gotKeys, expectedKeys)
	}
}

func TestParseModelIDsSupportsUnquotedClawManagerList(t *testing.T) {
	got, err := parseModelIDs("[auto,deepseek-v3.2,kimi-k2.5]")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"auto", "deepseek-v3.2", "kimi-k2.5"}
	if !equalStringSlices(got, expected) {
		t.Fatalf("unexpected model ids: got %v want %v", got, expected)
	}
}

func TestNormalizeLLMConfigDefaultsManagedProviderAPI(t *testing.T) {
	cfg := map[string]any{
		"models": map[string]any{
			"providers": map[string]any{
				"auto": map[string]any{
					"baseUrl": "https://legacy.example/v1",
					"apiKey":  "legacy-api-key",
				},
			},
		},
	}

	normalizeLLMConfigContent(cfg, llmOverrides{
		BaseURL:   "https://gateway.example/v1",
		APIKey:    "proxy-token",
		APIKeySet: true,
		ModelIDs:  []string{"auto"},
	})

	provider := nestedMapForTest(t, cfg, "models", "providers", "auto")
	if got := provider["api"]; got != "openai-completions" {
		t.Fatalf("expected managed provider api default, got %#v", got)
	}
	if got := provider["auth"]; got != "api-key" {
		t.Fatalf("expected explicit config api-key auth, got %#v", got)
	}
	if got := provider["baseUrl"]; got != "https://gateway.example/v1" {
		t.Fatalf("expected injected gateway baseUrl, got %#v", got)
	}
	if got := provider["apiKey"]; got != "proxy-token" {
		t.Fatalf("expected injected apiKey, got %#v", got)
	}
}

func TestNormalizeConfigSkipsLLMWhenNoOverrides(t *testing.T) {
	t.Setenv("CLAWMANAGER_LLM_MODEL", "")
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	unsetEnvForTest(t, "CLAWMANAGER_LLM_API_KEY")
	unsetEnvForTest(t, "OPENAI_API_KEY")

	content := []byte(`{"models":{"providers":{"auto":{"baseUrl":"https://legacy.example/v1"}}}}`)
	normalized, _, err := normalizeConfigMap(content, appconfig.Config{})
	if err != nil {
		t.Fatal(err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(normalized, &cfg); err != nil {
		t.Fatal(err)
	}
	providers := nestedMapForTest(t, cfg, "models", "providers")
	if _, ok := providers["auto"]; !ok {
		t.Fatalf("expected legacy auto provider to stay untouched, got %#v", providers)
	}
	if _, ok := providers["openai"]; ok {
		t.Fatalf("did not expect managed provider without LLM env overrides, got %#v", providers)
	}
}

func TestApplyRevisionUsesClawManagerLLMEnv(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "active", "openclaw.json")
	stateDir := filepath.Join(root, "state")

	st, err := store.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWMANAGER_LLM_MODEL", "gpt-4.1")
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "https://gateway.example/v1")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "proxy-token")

	manager := New(appconfig.Config{
		AgentDataDir:                 filepath.Join(root, "agent-data"),
		OpenClawConfigPath:           configPath,
		OpenClawBundledExtensionsDir: t.TempDir(),
		OpenClawExtensionsDir:        t.TempDir(),
		InstalledPluginPathPrefix:    "/does-not-exist/",
	}, stubRevisionClient{
		resp: protocol.ConfigRevisionResponse{
			Content: []byte(sampleOpenClawConfig),
		},
	}, st)

	if _, err := manager.ApplyRevision(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	provider := nestedMapForTest(t, cfg, "models", "providers", "auto")

	if got := provider["baseUrl"]; got != "https://gateway.example/v1" {
		t.Fatalf("expected ClawManager LLM baseUrl, got %#v", got)
	}
	if got := provider["apiKey"]; got != "proxy-token" {
		t.Fatalf("expected ClawManager LLM apiKey, got %#v", got)
	}
	if got := provider["auth"]; got != "api-key" {
		t.Fatalf("expected explicit config api-key auth, got %#v", got)
	}

	expectedModels := []string{"gpt-4.1"}
	if got := providerModelIDsForTest(t, provider); !equalStringSlices(got, expectedModels) {
		t.Fatalf("unexpected provider models: got %v want %v", got, expectedModels)
	}

	defaults := nestedMapForTest(t, cfg, "agents", "defaults")
	model := nestedMapForTest(t, defaults, "model")
	if got := model["primary"]; got != "auto/gpt-4.1" {
		t.Fatalf("expected primary auto/gpt-4.1, got %#v", got)
	}

	gotKeys := mapKeysForTest(t, defaults["models"])
	expectedKeys := []string{"auto/gpt-4.1"}
	if !equalStringSlices(gotKeys, expectedKeys) {
		t.Fatalf("unexpected agent models keys: got %v want %v", gotKeys, expectedKeys)
	}
}

func TestApplyRevisionPreservesDynamicAutoProviderConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "active", "openclaw.json")
	stateDir := filepath.Join(root, "state")

	st, err := store.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWMANAGER_LLM_MODEL", "")
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "")
	unsetEnvForTest(t, "CLAWMANAGER_LLM_API_KEY")

	revisionConfig := strings.ReplaceAll(sampleOpenClawConfig, "https://legacy.example/v1", "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm")
	revisionConfig = strings.ReplaceAll(revisionConfig, "legacy-api-key", "igt_dynamic_test")
	revisionConfig = strings.ReplaceAll(revisionConfig, "legacy-model", "auto")
	manager := New(appconfig.Config{
		AgentDataDir:       filepath.Join(root, "agent-data"),
		OpenClawConfigPath: configPath,
	}, stubRevisionClient{
		resp: protocol.ConfigRevisionResponse{
			Content: []byte(revisionConfig),
		},
	}, st)

	if _, err := manager.ApplyRevision(context.Background(), "dynamic"); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	provider := nestedMapForTest(t, cfg, "models", "providers", "auto")
	if got := provider["baseUrl"]; got != "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm" {
		t.Fatalf("expected dynamic ClawManager gateway baseUrl, got %#v", got)
	}
	if got := provider["apiKey"]; got != "igt_dynamic_test" {
		t.Fatalf("expected dynamic ClawManager apiKey, got %#v", got)
	}
	if got := provider["auth"]; got != "api-key" {
		t.Fatalf("expected explicit config api-key auth, got %#v", got)
	}
	model := nestedMapForTest(t, cfg, "agents", "defaults", "model")
	if got := model["primary"]; got != "auto/auto" {
		t.Fatalf("expected dynamic primary auto/auto, got %#v", got)
	}
}

func TestChannelsMergeRejectsUnknownIds(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	bundledDir := filepath.Join(root, "bundled-extensions")
	userDir := filepath.Join(root, "user-extensions")

	if err := os.MkdirAll(filepath.Join(bundledDir, "dingtalk-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"channels":["dingtalk"]}`)
	if err := os.WriteFile(filepath.Join(bundledDir, "dingtalk-plugin", "openclaw.plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWMANAGER_OPENCLAW_CHANNELS_JSON", `{"dingtalk":{"enabled":true},"slack":{"enabled":true}}`)
	t.Setenv("CLAWMANAGER_LLM_MODEL", "")
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "")
	os.Unsetenv("CLAWMANAGER_LLM_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	manager := New(appconfig.Config{
		OpenClawConfigPath:           configPath,
		OpenClawBundledExtensionsDir: bundledDir,
		OpenClawExtensionsDir:        userDir,
	}, nil, nil)
	if err := manager.NormalizeActiveConfig(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	channels := nestedMapForTest(t, cfg, "channels")
	if _, ok := channels["dingtalk"]; !ok {
		t.Fatalf("expected dingtalk channel to be preserved, got %#v", channels)
	}
	if _, ok := channels["slack"]; ok {
		t.Fatalf("expected unsupported slack channel to be dropped, got %#v", channels)
	}
}

func TestChannelsMergeAcceptsRegistryInstalledNPMChannels(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config", ".openclaw")
	defaultsRoot := filepath.Join(root, "defaults", ".openclaw")
	configPath := filepath.Join(configRoot, "openclaw.json")
	registryPath := filepath.Join(configRoot, "plugins", "installs.json")
	dingtalkDefaultsDir := filepath.Join(defaultsRoot, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	wecomDefaultsDir := filepath.Join(defaultsRoot, "npm", "node_modules", "@wecom", "wecom-openclaw-plugin")
	dingtalkConfigDir := filepath.Join(configRoot, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	wecomConfigDir := filepath.Join(configRoot, "npm", "node_modules", "@wecom", "wecom-openclaw-plugin")

	for _, dir := range []string{dingtalkConfigDir, wecomConfigDir, filepath.Dir(registryPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dingtalkConfigDir, "openclaw.plugin.json"), []byte(`{"channels":["dingtalk-connector"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wecomConfigDir, "openclaw.plugin.json"), []byte(`{"channels":["wecom"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	registry, err := json.Marshal(map[string]any{
		"plugins": []map[string]any{
			{
				"pluginId":     "dingtalk-connector",
				"manifestPath": filepath.Join(dingtalkDefaultsDir, "openclaw.plugin.json"),
				"rootDir":      dingtalkDefaultsDir,
			},
			{
				"pluginId":     "wecom-openclaw-plugin",
				"manifestPath": filepath.Join(wecomDefaultsDir, "openclaw.plugin.json"),
				"rootDir":      wecomDefaultsDir,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWMANAGER_OPENCLAW_CHANNELS_JSON", `{"dingtalk-connector":{"enabled":true},"wecom":{"enabled":true},"slack":{"enabled":true}}`)
	t.Setenv("CLAWMANAGER_LLM_MODEL", "")
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "")
	unsetEnvForTest(t, "CLAWMANAGER_LLM_API_KEY")
	unsetEnvForTest(t, "OPENAI_API_KEY")

	manager := New(appconfig.Config{
		OpenClawConfigPath:           configPath,
		OpenClawBundledExtensionsDir: t.TempDir(),
		OpenClawExtensionsDir:        t.TempDir(),
		OpenClawDefaultsDir:          defaultsRoot,
	}, nil, nil)
	if err := manager.NormalizeActiveConfig(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	channels := nestedMapForTest(t, cfg, "channels")
	if _, ok := channels["dingtalk-connector"]; !ok {
		t.Fatalf("expected registry dingtalk channel to be preserved, got %#v", channels)
	}
	if _, ok := channels["wecom"]; !ok {
		t.Fatalf("expected registry wecom channel to be preserved, got %#v", channels)
	}
	if _, ok := channels["slack"]; ok {
		t.Fatalf("expected unsupported slack channel to be dropped, got %#v", channels)
	}
}

func TestPluginInstallPathRewritten(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	userDir := "/config/.openclaw/extensions"

	sample := `{
		"plugins": {
			"installs": {
				"foo": {
					"installPath": "/defaults/.openclaw/extensions/foo",
					"manifestPath": "/defaults/.openclaw/extensions/foo/openclaw.plugin.json",
					"metadata": {
						"manifestCandidates": [
							"/defaults/.openclaw/extensions/foo/openclaw.plugin.json"
						]
					}
				},
				"bar": {
					"installPath": "/opt/vendor/bar"
				}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("CLAWMANAGER_OPENCLAW_CHANNELS_JSON")
	os.Unsetenv("CLAWMANAGER_LLM_MODEL")
	os.Unsetenv("CLAWMANAGER_LLM_BASE_URL")
	os.Unsetenv("CLAWMANAGER_LLM_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	manager := New(appconfig.Config{
		OpenClawConfigPath:        configPath,
		OpenClawExtensionsDir:     userDir,
		InstalledPluginPathPrefix: "/defaults/.openclaw/extensions/",
	}, nil, nil)
	if err := manager.NormalizeActiveConfig(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	installs := nestedMapForTest(t, cfg, "plugins", "installs")
	foo, _ := installs["foo"].(map[string]any)
	bar, _ := installs["bar"].(map[string]any)
	wantFoo := path.Join(userDir, "foo")
	if got, _ := foo["installPath"].(string); got != wantFoo {
		t.Fatalf("expected foo installPath to be rewritten to %q, got %q", wantFoo, got)
	}
	wantManifest := path.Join(userDir, "foo", "openclaw.plugin.json")
	if got, _ := foo["manifestPath"].(string); got != wantManifest {
		t.Fatalf("expected foo manifestPath to be rewritten to %q, got %q", wantManifest, got)
	}
	metadata := nestedMapForTest(t, foo, "metadata")
	candidates, _ := metadata["manifestCandidates"].([]any)
	if len(candidates) != 1 || candidates[0] != wantManifest {
		t.Fatalf("expected nested manifest candidates to be rewritten to %q, got %#v", wantManifest, candidates)
	}
	if got, _ := bar["installPath"].(string); got != "/opt/vendor/bar" {
		t.Fatalf("expected bar installPath to be untouched, got %q", got)
	}
}

func TestNormalizeActiveConfigRewritesPluginInstallRegistry(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	registryPath := filepath.Join(root, "plugins", "installs.json")
	userDir := "/config/.openclaw/extensions"
	configRoot := pathClean(filepath.Dir(configPath))
	defaultsRoot := "/defaults/.openclaw"

	config := `{
    "channels": {},
    "plugins": {
        "entries": {}
    }
}
`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `{
    "installRecords": {
        "dingtalk-connector": {
            "installPath": "/defaults/.openclaw/extensions/dingtalk-connector"
        },
        "wecom-openclaw-plugin": {
            "installPath": "/defaults/.openclaw/npm/node_modules/@wecom/wecom-openclaw-plugin"
        }
    },
    "plugins": [
        {
            "pluginId": "dingtalk-connector",
            "manifestPath": "/defaults/.openclaw/extensions/dingtalk-connector/openclaw.plugin.json",
            "source": "/defaults/.openclaw/extensions/dingtalk-connector/dist/index.mjs",
            "rootDir": "/defaults/.openclaw/extensions/dingtalk-connector"
        },
        {
            "pluginId": "wecom-openclaw-plugin",
            "manifestPath": "/defaults/.openclaw/npm/node_modules/@wecom/wecom-openclaw-plugin/openclaw.plugin.json",
            "source": "/defaults/.openclaw/npm/node_modules/@wecom/wecom-openclaw-plugin/dist/index.js",
            "rootDir": "/defaults/.openclaw/npm/node_modules/@wecom/wecom-openclaw-plugin"
        }
    ]
}
`
	if err := os.WriteFile(registryPath, []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{
		OpenClawConfigPath:           configPath,
		OpenClawBundledExtensionsDir: t.TempDir(),
		OpenClawExtensionsDir:        userDir,
		OpenClawDefaultsDir:          defaultsRoot,
		InstalledPluginPathPrefix:    "/defaults/.openclaw/extensions/",
	}, nil, nil)
	if err := manager.NormalizeActiveConfig(); err != nil {
		t.Fatal(err)
	}

	got := readConfigForTest(t, registryPath)
	record := nestedMapForTest(t, got, "installRecords", "dingtalk-connector")
	if gotPath, _ := record["installPath"].(string); gotPath != path.Join(userDir, "dingtalk-connector") {
		t.Fatalf("expected install registry path to be rewritten, got %q", gotPath)
	}
	wecomRecord := nestedMapForTest(t, got, "installRecords", "wecom-openclaw-plugin")
	wantWecomRoot := path.Join(configRoot, "npm", "node_modules", "@wecom", "wecom-openclaw-plugin")
	if gotPath, _ := wecomRecord["installPath"].(string); gotPath != wantWecomRoot {
		t.Fatalf("expected npm install registry path to be rewritten to %q, got %q", wantWecomRoot, gotPath)
	}
	plugins, _ := got["plugins"].([]any)
	if len(plugins) != 2 {
		t.Fatalf("expected two plugin entries, got %#v", plugins)
	}
	plugin, _ := plugins[0].(map[string]any)
	wantRoot := path.Join(userDir, "dingtalk-connector")
	if gotPath, _ := plugin["rootDir"].(string); gotPath != wantRoot {
		t.Fatalf("expected rootDir to be rewritten to %q, got %q", wantRoot, gotPath)
	}
	if gotPath, _ := plugin["manifestPath"].(string); gotPath != path.Join(wantRoot, "openclaw.plugin.json") {
		t.Fatalf("expected manifestPath to be rewritten, got %q", gotPath)
	}
	if gotPath, _ := plugin["source"].(string); gotPath != path.Join(wantRoot, "dist", "index.mjs") {
		t.Fatalf("expected source to be rewritten, got %q", gotPath)
	}
	wecomPlugin, _ := plugins[1].(map[string]any)
	if gotPath, _ := wecomPlugin["rootDir"].(string); gotPath != wantWecomRoot {
		t.Fatalf("expected npm rootDir to be rewritten to %q, got %q", wantWecomRoot, gotPath)
	}
	if gotPath, _ := wecomPlugin["manifestPath"].(string); gotPath != path.Join(wantWecomRoot, "openclaw.plugin.json") {
		t.Fatalf("expected npm manifestPath to be rewritten, got %q", gotPath)
	}
	if gotPath, _ := wecomPlugin["source"].(string); gotPath != path.Join(wantWecomRoot, "dist", "index.js") {
		t.Fatalf("expected npm source to be rewritten, got %q", gotPath)
	}
}

type stubRevisionClient struct {
	resp protocol.ConfigRevisionResponse
	err  error
}

func (s stubRevisionClient) FetchConfigRevision(context.Context, string) (protocol.ConfigRevisionResponse, error) {
	return s.resp, s.err
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func readConfigForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func nestedMapForTest(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	current := root
	for _, part := range path {
		next, ok := current[part].(map[string]any)
		if !ok {
			t.Fatalf("expected object at %v, got %#v", path, current[part])
		}
		current = next
	}
	return current
}

func providerModelIDsForTest(t *testing.T, provider map[string]any) []string {
	t.Helper()
	items, ok := provider["models"].([]any)
	if !ok {
		t.Fatalf("expected provider models array, got %#v", provider["models"])
	}
	modelIDs := make([]string, 0, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected model object, got %#v", item)
		}
		id, ok := model["id"].(string)
		if !ok {
			t.Fatalf("expected string model id, got %#v", model["id"])
		}
		modelIDs = append(modelIDs, id)
	}
	return modelIDs
}

func mapKeysForTest(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected model map, got %#v", value)
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const sampleOpenClawConfig = `{
    "models": {
        "mode": "merge",
        "providers": {
            "auto": {
                "baseUrl": "https://legacy.example/v1",
                "apiKey": "legacy-api-key",
                "api": "openai-completions",
                "models": [
                    {
                        "id": "legacy-model",
                        "name": "Legacy Model",
                        "reasoning": false,
                        "input": [
                            "text"
                        ],
                        "cost": {
                            "input": 0,
                            "output": 0,
                            "cacheRead": 0,
                            "cacheWrite": 0
                        },
                        "contextWindow": 64000,
                        "maxTokens": 8192
                    }
                ]
            }
        }
    },
    "agents": {
        "defaults": {
            "model": {
                "primary": "auto/legacy-model"
            },
            "models": {
                "auto/legacy-model": {}
            }
        }
    }
}`
