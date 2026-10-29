package hermes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"gopkg.in/yaml.v3"
)

func desktopWebConfigFixture(t *testing.T) (gateway.Config, gateway.CreateGatewayRequest, string) {
	t.Helper()
	t.Setenv("CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED", "true")
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-45", "instance-63")
	return gateway.Config{RuntimeType: "hermes", WorkspaceRoot: root, PublicOrigin: "http://clawmanager-backend.platform.svc:9001", TrustedProxies: []string{"10.23.0.0/24"}}, gateway.CreateGatewayRequest{
		AgentType: "hermes", UserID: 45, InstanceID: 63, Generation: 1,
		UID: os.Getuid(), GID: os.Getgid(), WorkspacePath: workspace,
		Environment: map[string]string{
			"CLAWMANAGER_LLM_BASE_URL":   "http://clawmanager-gateway.platform.svc:9001/api/v1/gateway/llm",
			"CLAWMANAGER_LLM_API_KEY":    "instance-63-llm-token",
			"CLAWMANAGER_LLM_MODEL":      `["auto","model: with # punctuation"]`,
			"CLAWMANAGER_INSTANCE_TOKEN": "instance-63-auth-token",
		},
	}, workspace
}

func TestManagedDesktopWebPolicyCannotBeOverriddenOrPersisted(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	cfg.ProxyPolicyFile = gateway.ManagedProxyPolicyFile
	cfg.Namespace = "tenant"
	req.Environment["CLAWMANAGER_PROXY_POLICY_FILE"] = "/workspace/forged"
	req.Environment["CLAWMANAGER_PROXY_POLICY_NAMESPACE"] = "other"
	req.Environment["CLAWMANAGER_TRUSTED_PROXY_CIDRS"] = "0.0.0.0/0"
	_, proxies, err := desktopWebProxyConfig(cfg, req)
	if err != nil || len(proxies) != 0 {
		t.Fatalf("managed trust leaked into config: %v %v", proxies, err)
	}
	env := desktopWebEnvironment(nil, cfg, req, workspace, 20000)
	if envValue(env, "CLAWMANAGER_PROXY_POLICY_FILE") != cfg.ProxyPolicyFile || envValue(env, "CLAWMANAGER_PROXY_POLICY_NAMESPACE") != cfg.Namespace || envValue(env, "CLAWMANAGER_TRUSTED_PROXY_CIDRS") != "" {
		t.Fatal("request overrode managed trust identity")
	}
}

func TestDesktopWebConfigMigratesAndPreservesUserData(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	hermesHome := filepath.Join(workspace, "home", ".hermes")
	if err := os.MkdirAll(hermesHome, 0o750); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{
		"config.yaml":  "# user preferences\nmodel:\n  temperature: 0.25\n  api_key: old-model-secret\nproviders:\n  clawmanager:\n    api_key: old-provider-secret\n    context_length: 100000\n    models:\n      auto:\n        supports_tools: true\n  local-user-model:\n    base_url: http://localhost:1234\ndashboard:\n  theme: dark\n  public_url: https://stale.example\ncustom_setting:\n  nested: keep\n",
		".env":         "# user integration\nMY_CUSTOM_KEY='user value # stays'\nexport OPENAI_API_KEY=old-key\nOPENAI_API_KEY=duplicate-old-key\nOTHER_KEY=123\n",
		"gateway.json": `{"base_path":"/old","user_feature":{"enabled":true},"large_id":1234567890123456789}`,
		"state.db":     "existing session data",
	}
	for name, data := range before {
		if err := os.WriteFile(filepath.Join(hermesHome, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
			t.Fatalf("write attempt %d: %v", attempt, err)
		}
	}
	var actual map[string]any
	data, err := os.ReadFile(filepath.Join(hermesHome, "config.yaml"))
	if err != nil || yaml.Unmarshal(data, &actual) != nil {
		t.Fatalf("read valid merged YAML: %v", err)
	}
	model := actual["model"].(map[string]any)
	if model["temperature"] != 0.25 || model["api_key"] != "" || model["provider"] != "clawmanager" || model["key_env"] != "OPENAI_API_KEY" {
		t.Fatalf("incorrect merged model routing: %#v", model)
	}
	provider := actual["providers"].(map[string]any)["clawmanager"].(map[string]any)
	if provider["context_length"] != 100000 || provider["api_key"] != "" || provider["models"].(map[string]any)["auto"].(map[string]any)["supports_tools"] != true {
		t.Fatalf("provider metadata lost: %#v", provider)
	}
	if _, exists := provider["models"].(map[string]any)["model: with # punctuation"]; !exists {
		t.Fatal("managed model identifier did not survive YAML encoding")
	}
	dashboard := actual["dashboard"].(map[string]any)
	if dashboard["theme"] != "dark" || dashboard["public_url"] != cfg.PublicOrigin+"/api/v1/instances/63/proxy" {
		t.Fatalf("invalid Dashboard configuration: %#v", dashboard)
	}
	for _, name := range []string{"config.yaml", ".env", "gateway.json"} {
		backup, err := os.ReadFile(filepath.Join(hermesHome, name+".clawmanager-pre-desktop-web.bak"))
		if err != nil || string(backup) != before[name] {
			t.Fatalf("original %s backup changed: %v", name, err)
		}
	}
	env, _ := os.ReadFile(filepath.Join(hermesHome, ".env"))
	if !strings.Contains(string(env), "MY_CUSTOM_KEY='user value # stays'") || !strings.Contains(string(env), "OTHER_KEY=123") || strings.Count(string(env), "\nOPENAI_API_KEY=") != 1 || strings.Contains(string(env), "old-key") {
		t.Fatal(".env merge lost custom fields or retained stale managed credentials")
	}
	gatewayData, _ := os.ReadFile(filepath.Join(hermesHome, "gateway.json"))
	if !strings.Contains(string(gatewayData), "1234567890123456789") || !strings.Contains(string(gatewayData), `"user_feature"`) {
		t.Fatal("gateway.json merge lost user fields or integer precision")
	}
	sessions, _ := os.ReadFile(filepath.Join(hermesHome, "state.db"))
	if string(sessions) != before["state.db"] {
		t.Fatal("session data was changed")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"config.yaml", ".env", "gateway.json", desktopWebMarkerFile, ".env.clawmanager-pre-desktop-web.bak"} {
			info, err := os.Stat(filepath.Join(hermesHome, name))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("sensitive file %s is not 0600: %v", name, err)
			}
		}
	}
}

func TestDesktopWebConfigPersistsExistingTeamContract(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	req.Environment["CLAWMANAGER_TEAM_ENABLED"] = "true"
	req.Environment["CLAWMANAGER_TEAM_CONFIG_JSON"] = `{"teamId":"42","memberId":"leader"}`
	req.Environment["CLAWMANAGER_TEAM_SHARED_DIR"] = "/team"

	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "team", "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if actual["teamId"] != "42" || actual["memberId"] != "leader" {
		t.Fatalf("unexpected Team contract: %#v", actual)
	}
}

func TestDesktopWebConfigDoesNotUseDeploymentLLMCredentials(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	cfg.LLMAPIKey, cfg.LLMAPIKeySet = "pod-global-token", true
	cfg.LLMBaseURL = "http://global-provider.example"
	t.Setenv("OPENAI_API_KEY", "pod-env-token")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "pod-env-token")
	req.Environment["CLAWMANAGER_TEAM_ENABLED"] = "true"
	req.Environment["CLAWMANAGER_TEAM_CONFIG_JSON"] = `{"teamId":"42"}`
	delete(req.Environment, "CLAWMANAGER_LLM_API_KEY")
	if err := WriteGatewayConfig(cfg, req, workspace); err == nil || strings.Contains(err.Error(), "pod-env-token") {
		t.Fatal("missing per-instance credentials did not fail safely")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("invalid credentials caused workspace writes")
	}
	req.Environment["CLAWMANAGER_LLM_API_KEY"] = "request-token"
	delete(req.Environment, "CLAWMANAGER_LLM_BASE_URL")
	if err := WriteGatewayConfig(cfg, req, workspace); err == nil {
		t.Fatal("missing per-instance endpoint fell back to deployment configuration")
	}
}

func TestDesktopWebConfigMigratesDisabledBasicAuthAndRetiresOldCredentials(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	hermesHome := filepath.Join(workspace, "home", ".hermes")
	if err := os.MkdirAll(hermesHome, 0o750); err != nil {
		t.Fatal(err)
	}
	before := []byte("# existing plugin choices\nshared_disabled: &disabled\n  - basic\n  - dashboard_auth/basic\n  - other-plugin # keep this disabled\nplugins:\n  disabled: *disabled\n  enabled: []\n  entries:\n    other-plugin:\n      user_setting: keep\ndashboard:\n  basic_auth:\n    username: old-admin\n    password: old-plaintext\n    password_hash: old-password-hash\n    secret: preserve-signing-secret\n    session_ttl_seconds: 1200\n")
	if err := os.WriteFile(filepath.Join(hermesHome, "config.yaml"), before, 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
			t.Fatalf("auth migration attempt %d: %v", attempt, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(hermesHome, "config.yaml"))
	var actual map[string]any
	if err != nil || yaml.Unmarshal(data, &actual) != nil {
		t.Fatal("migrated config is not readable YAML")
	}
	auth := actual["dashboard"].(map[string]any)["basic_auth"].(map[string]any)
	if auth["password"] != "" || auth["password_hash"] != "" || auth["secret"] != "preserve-signing-secret" || auth["session_ttl_seconds"] != 1200 {
		t.Fatal("managed auth migration retained stale passwords or replaced unrelated metadata")
	}
	plugins := actual["plugins"].(map[string]any)
	disabled := plugins["disabled"].([]any)
	if len(disabled) != 1 || disabled[0] != "other-plugin" || len(plugins["enabled"].([]any)) != 0 {
		t.Fatal("auth migration changed unrelated plugin enablement")
	}
	if plugins["entries"].(map[string]any)["other-plugin"].(map[string]any)["user_setting"] != "keep" || !strings.Contains(string(data), "keep this disabled") {
		t.Fatal("plugin metadata or comments were lost")
	}
	if shared := actual["shared_disabled"].([]any); len(shared) != 3 {
		t.Fatal("editing the disabled-list alias changed unrelated user configuration")
	}
	env, err := os.ReadFile(filepath.Join(hermesHome, ".env"))
	if err != nil || !strings.Contains(string(env), "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=instance-63-auth-token") {
		t.Fatal("managed dashboard credentials were not written")
	}
	backup, err := os.ReadFile(filepath.Join(hermesHome, "config.yaml.clawmanager-pre-desktop-web.bak"))
	if err != nil || string(backup) != string(before) {
		t.Fatal("original auth configuration was not preserved for rollback")
	}
}

func TestDesktopWebConfigRejectsIncompatibleWorkspaceBeforeWrites(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	hermesHome := filepath.Join(workspace, "home", ".hermes")
	before, _ := os.ReadFile(filepath.Join(hermesHome, ".env"))
	marker, _ := json.Marshal(desktopWebWorkspaceVersion{SchemaVersion: 999, HermesVersion: "unknown"})
	if err := os.WriteFile(filepath.Join(hermesHome, desktopWebMarkerFile), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	req.Environment["CLAWMANAGER_LLM_API_KEY"] = "new-token"
	if err := WriteGatewayConfig(cfg, req, workspace); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible workspace accepted: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(hermesHome, ".env"))
	if string(before) != string(after) {
		t.Fatal("incompatible workspace credentials overwritten")
	}
}

func TestDesktopWebConfigRejectsMalformedFilesWithoutPartialRewrite(t *testing.T) {
	for _, name := range []string{"config.yaml", "gateway.json"} {
		t.Run(name, func(t *testing.T) {
			cfg, req, workspace := desktopWebConfigFixture(t)
			hermesHome := filepath.Join(workspace, "home", ".hermes")
			if err := os.MkdirAll(hermesHome, 0o750); err != nil {
				t.Fatal(err)
			}
			before := []byte("must-preserve-secret: [")
			if err := os.WriteFile(filepath.Join(hermesHome, name), before, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := WriteGatewayConfig(cfg, req, workspace); err == nil || strings.Contains(err.Error(), "must-preserve-secret") {
				t.Fatalf("invalid file did not fail without secret details: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(hermesHome, name))
			if string(before) != string(after) {
				t.Fatal("invalid file overwritten")
			}
			if _, err := os.Stat(filepath.Join(hermesHome, ".env")); !os.IsNotExist(err) {
				t.Fatal("validation failure partially wrote credentials")
			}
		})
	}
}

func TestDesktopWebConfigRejectsSymlinkEscapes(t *testing.T) {
	for _, target := range []string{"hermes", "user", "instance", "home", ".hermes", "config.yaml", ".env", "gateway.json", desktopWebMarkerFile} {
		t.Run(target, func(t *testing.T) {
			cfg, req, workspace := desktopWebConfigFixture(t)
			outside := t.TempDir()
			hermesHome := filepath.Join(workspace, "home", ".hermes")
			link := filepath.Join(hermesHome, target)
			directory := true
			if target == "hermes" {
				link = filepath.Join(cfg.WorkspaceRoot, "hermes")
			} else if target == "user" {
				link = filepath.Dir(workspace)
			} else if target == "instance" {
				link = workspace
			} else if target == "home" {
				link = filepath.Join(workspace, "home")
			} else if target == ".hermes" {
				link = hermesHome
			} else {
				directory = false
				outside = filepath.Join(outside, "outside-file")
				if err := os.WriteFile(outside, []byte("do-not-touch"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			before, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteGatewayConfig(cfg, req, workspace); err == nil {
				t.Fatalf("accepted symlink at %s", target)
			}
			after, err := os.Stat(outside)
			if err != nil || after.Mode() != before.Mode() {
				t.Fatal("modified permissions outside managed workspace")
			}
			if !directory {
				data, _ := os.ReadFile(outside)
				if string(data) != "do-not-touch" {
					t.Fatal("modified outside file")
				}
			}
		})
	}
}

func TestDesktopWebConfigRejectsUnboundedProxyTrust(t *testing.T) {
	cfg, req, workspace := desktopWebConfigFixture(t)
	for _, proxy := range []string{"*", "0.0.0.0/0", "::/0", "not-a-network"} {
		cfg.TrustedProxies = []string{proxy}
		if err := WriteGatewayConfig(cfg, req, workspace); err == nil {
			t.Fatalf("accepted unsafe proxy %q", proxy)
		}
	}
	cfg.TrustedProxies = nil
	for _, origin := range []string{"*", "https://*.example", "https://example/path", "http://secret@example", "https://example?secret=value"} {
		cfg.PublicOrigin = origin
		if err := WriteGatewayConfig(cfg, req, workspace); err == nil {
			t.Fatalf("accepted unsafe origin %q", origin)
		}
	}
}

func TestDesktopWebConcurrentInstancesKeepSeparateCredentials(t *testing.T) {
	cfg, first, firstWorkspace := desktopWebConfigFixture(t)
	second := first
	second.InstanceID = 64
	second.WorkspacePath = filepath.Join(cfg.WorkspaceRoot, "hermes", "user-45", "instance-64")
	second.Environment = map[string]string{}
	for key, value := range first.Environment {
		second.Environment[key] = value
	}
	second.Environment["CLAWMANAGER_LLM_API_KEY"] = "instance-64-llm-token"
	second.Environment["CLAWMANAGER_INSTANCE_TOKEN"] = "instance-64-auth-token"
	var group sync.WaitGroup
	for _, req := range []gateway.CreateGatewayRequest{first, second} {
		group.Go(func() {
			if err := WriteGatewayConfig(cfg, req, req.WorkspacePath); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		})
	}
	group.Wait()
	firstEnv, _ := os.ReadFile(filepath.Join(firstWorkspace, "home", ".hermes", ".env"))
	secondEnv, _ := os.ReadFile(filepath.Join(second.WorkspacePath, "home", ".hermes", ".env"))
	if !strings.Contains(string(firstEnv), "instance-63-llm-token") || strings.Contains(string(firstEnv), "instance-64") || !strings.Contains(string(secondEnv), "instance-64-llm-token") || strings.Contains(string(secondEnv), "instance-63") {
		t.Fatal("concurrent instance credentials were mixed")
	}
}

func TestDesktopWebMigrationPreservesUnknownLegacyManagedFields(t *testing.T) {
	before := []byte("model:\n  temperature: 0.5\n" + managedConfigStart + "\nmodel:\n  default: old\n  user_extension: keep\nproviders:\n  clawmanager:\n    context_length: 90000\n" + managedConfigEnd + "\n")
	merged, err := mergeDesktopWebYAML(before, gateway.Config{LLMBaseURL: "http://gateway.svc/v1", LLMModelIDs: []string{"auto"}}, "http://backend.svc/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := yaml.Unmarshal(merged, &result); err != nil {
		t.Fatal(err)
	}
	model := result["model"].(map[string]any)
	if model["temperature"] != 0.5 || model["user_extension"] != "keep" || model["default"] != "auto" {
		t.Fatalf("legacy nested fields lost during migration: %#v", model)
	}
}

func TestDesktopWebEnvPreservesMultilineUserValues(t *testing.T) {
	before := []byte("USER_MULTILINE=\"first\nOPENAI_API_KEY=part-of-user-value\nlast\"\nOPENAI_API_KEY=old-secret\n")
	merged, err := mergeDesktopWebEnv(before, map[string]string{"OPENAI_API_KEY": "instance-token"})
	if err != nil || !strings.Contains(string(merged), "USER_MULTILINE=\"first\nOPENAI_API_KEY=part-of-user-value\nlast\"") || strings.Contains(string(merged), "old-secret") {
		t.Fatalf("multiline env merge failed: %v", err)
	}
	if _, err := mergeDesktopWebEnv([]byte("USER_VALUE=\"unclosed\n"), map[string]string{"OPENAI_API_KEY": "instance-token"}); err == nil {
		t.Fatal("unterminated env value could swallow managed credentials")
	}
}

func TestDesktopWebMergePreservesAliasValuesOutsideManagedFields(t *testing.T) {
	for _, before := range []string{
		"defaults: &defaults\n  temperature: 0.25\n  default: old\nmodel: *defaults\n",
		"model: &defaults\n  temperature: 0.25\n  default: old\nuser_backup: *defaults\n",
	} {
		merged, err := mergeDesktopWebYAML([]byte(before), gateway.Config{LLMBaseURL: "http://gateway.svc/v1", LLMModelIDs: []string{"auto"}}, "http://backend.svc/proxy", nil)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(merged, &values); err != nil {
			t.Fatal(err)
		}
		model := values["model"].(map[string]any)
		if model["temperature"] != 0.25 || model["default"] != "auto" {
			t.Fatal("lost inherited model setting")
		}
		for _, key := range []string{"defaults", "user_backup"} {
			if backup, ok := values[key].(map[string]any); ok && backup["default"] != "old" {
				t.Fatal("managed overlay mutated an unrelated alias")
			}
		}
	}
}

func TestDesktopWebMergeFlattensInheritedPluginsAndProviderMetadata(t *testing.T) {
	before := []byte(`plugin_defaults: &plugin_defaults
  disabled: [basic, dashboard_auth/basic, user-plugin]
provider_defaults: &provider_defaults
  clawmanager:
    context_length: 12345
    models:
      auto:
        supports_tools: true
plugins:
  <<: *plugin_defaults
providers:
  <<: *provider_defaults
`)
	merged, err := mergeDesktopWebYAML(before, gateway.Config{LLMBaseURL: "http://gateway.platform.svc/v1", LLMModelIDs: []string{"auto"}}, "http://backend.platform.svc/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := yaml.Unmarshal(merged, &result); err != nil {
		t.Fatal(err)
	}
	disabled := result["plugins"].(map[string]any)["disabled"].([]any)
	if len(disabled) != 1 || disabled[0] != "user-plugin" {
		t.Fatalf("inherited Basic auth denial survived merge: %#v", disabled)
	}
	original := result["plugin_defaults"].(map[string]any)["disabled"].([]any)
	if len(original) != 3 || original[0] != "basic" {
		t.Fatal("enabling Basic auth changed unrelated anchor defaults")
	}
	provider := result["providers"].(map[string]any)["clawmanager"].(map[string]any)
	if provider["context_length"] != 12345 || provider["models"].(map[string]any)["auto"].(map[string]any)["supports_tools"] != true {
		t.Fatal("overlay discarded inherited provider metadata")
	}
}

func TestDesktopWebYAMLMergeKeepsExplicitAndSequencePrecedence(t *testing.T) {
	before := []byte(`first: &first {chosen: first, inherited: first, "<<": user-field}
second: &second {chosen: second, inherited: second, extra: second}
settings:
  chosen: explicit
  <<: [*first, *second]
`)
	document, err := parseDesktopWebYAML(before)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := document.Decode(&result); err != nil {
		t.Fatal(err)
	}
	settings := result["settings"].(map[string]any)
	if settings["chosen"] != "explicit" || settings["inherited"] != "first" || settings["extra"] != "second" || settings["<<"] != "user-field" {
		t.Fatalf("YAML merge precedence changed: %#v", settings)
	}
}

func TestDesktopWebQualifiedProvidersUseManagedCustomNamespace(t *testing.T) {
	before := []byte(`providers:
  clawmanager-auto:
    enabled: false
    api: https://stale-api.example/v1
    url: https://stale-url.example/v1
    key_cmd: old-credential-command
    context_length: 12345
`)
	cfg := gateway.Config{LLMBaseURL: "http://gateway.platform.svc/v1", LLMModelsQualified: true, LLMModelIDs: []string{"auto/auto", "deepseek/deepseek-v4-pro"}}
	merged, err := mergeDesktopWebYAML(before, cfg, "http://backend.platform.svc/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := yaml.Unmarshal(merged, &result); err != nil {
		t.Fatal(err)
	}
	model := result["model"].(map[string]any)
	if model["provider"] != "clawmanager-auto" || model["default"] != "auto" {
		t.Fatalf("auto model uses reserved upstream provider identity: %#v", model)
	}
	providers := result["providers"].(map[string]any)
	for name, modelID := range map[string]string{"clawmanager-auto": "auto", "clawmanager-deepseek": "deepseek-v4-pro"} {
		provider, exists := providers[name].(map[string]any)
		if !exists || provider["enabled"] != true || provider["base_url"] != cfg.LLMBaseURL || provider["key_env"] != "OPENAI_API_KEY" || provider["api_mode"] != "chat_completions" {
			t.Fatalf("managed custom provider is not configured: %s", name)
		}
		for _, alias := range []string{"api", "url", "key_cmd", "api_key"} {
			if provider[alias] != "" {
				t.Fatalf("managed route may be overridden by %s", alias)
			}
		}
		if _, exists := provider["models"].(map[string]any)[modelID]; !exists {
			t.Fatalf("model identifier changed: %s", modelID)
		}
	}
	if providers["clawmanager-auto"].(map[string]any)["context_length"] != 12345 {
		t.Fatal("managed provider lost unrelated metadata")
	}
	if _, exists := providers["auto"]; exists {
		t.Fatal("emitted upstream-reserved auto provider")
	}
	if _, exists := providers["deepseek"]; exists {
		t.Fatal("emitted upstream builtin deepseek provider")
	}
}
