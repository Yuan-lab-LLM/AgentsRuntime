package hermes

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedCredentialMigration(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	secret := rand.Text()
	custom := rand.Text()
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	homes := []string{"home/.hermes", "home/.clawmanager-team-worker/.hermes"}
	for _, rel := range homes {
		home := filepath.Join(workspace, rel)
		os.MkdirAll(home, 0700)
		files := map[string]string{"config.yaml": fmt.Sprintf("model:\n  base_url: http://backend.ns.svc/api/v1/gateway/llm\n  api_key: %s\nproviders:\n  clawmanager:\n    base_url: http://backend.ns.svc/api/v1/gateway/llm\n    api_key: %s\n  private:\n    api_key: %s\ndashboard:\n  basic_auth:\n    password: %s\n", secret, secret, custom, secret), ".env": fmt.Sprintf("OPENAI_API_KEY=%s\nexport OPENAI_API_KEY='%s'\nCLAWMANAGER_TEAM_TOKEN=%s\nCUSTOM_KEY=%s\nHERMES_DASHBOARD_BASIC_AUTH_PASSWORD=%s\nHERMES_DASHBOARD_BASIC_AUTH_SECRET=%s\n", secret, secret, secret, custom, secret, secret), "gateway.json": fmt.Sprintf(`{"auth":{"token":%q},"custom":%q}`, secret, custom)}
		for name, raw := range files {
			for _, suffix := range []string{"", ".clawmanager-pre-desktop-web.bak"} {
				os.WriteFile(filepath.Join(home, name+suffix), []byte(raw), 0600)
				if suffix == "" {
					os.WriteFile(filepath.Join(home, "."+name+".0123456789abcdef01234567.tmp"), []byte(raw), 0600)
				}
			}
		}
	}
	if err := NewProfile("hermes").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, rel := range homes {
		entries, _ := os.ReadDir(filepath.Join(workspace, rel))
		for _, entry := range entries {
			data, _ := os.ReadFile(filepath.Join(workspace, rel, entry.Name()))
			if strings.Contains(string(data), secret) || !strings.Contains(string(data), custom) {
				t.Fatalf("migration boundary failed %s %s", entry.Name(), data)
			}
		}
	}
}
func TestProtectedHermesCanonicalLocalModel(t *testing.T) {
	for _, endpoint := range []string{managedconfig.ModelURL, "http://clawmanager-local-model.invalid:31280/api/v1/gateway/llm", "http://clawmanager-local-model.invalid:31281/other", "http://clawmanager-local-model.invalid.evil:31281/api/v1/gateway/llm", managedconfig.ModelURL + "?x=1", managedconfig.ModelURL + "/"} {
		t.Run(endpoint, func(t *testing.T) {
			cfg := gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}
			req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": endpoint}}
			_, err := desktopWebLLMConfig(cfg, req)
			if (err == nil) != (endpoint == managedconfig.ModelURL) {
				t.Fatalf("endpoint result %v", err)
			}
			if endpoint == managedconfig.ModelURL {
				env := desktopWebEnvironment(nil, cfg, req, t.TempDir(), 20000)
				if !strings.Contains(strings.Join(env, "\n"), "OPENAI_BASE_URL="+endpoint) || !strings.Contains(strings.Join(env, "\n"), "CLAWMANAGER_MANAGED_SERVICES_URL="+managedconfig.ServicesURL) {
					t.Fatal("trusted local endpoints absent from native environment")
				}
				req.SecurityProfile = ""
				if _, err = desktopWebLLMConfig(cfg, req); err == nil {
					t.Fatal("untrusted alias accepted")
				}
			}
		})
	}
}

func TestProtectedHermesIgnoresLegacyPasswordAliases(t *testing.T) {
	cfg := gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "old-raw-token", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	env := strings.Join(desktopWebEnvironment(nil, cfg, req, t.TempDir(), 20000), "\n")
	if strings.Contains(env, "old-raw-token") || !strings.Contains(env, "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=ui1_new") {
		t.Fatal("legacy password won over protected UI credential")
	}
}

func TestProtectedHermesWritesCanonicalLocalModelConfig(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-42")
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", NetworkSecurityProfile: "isolated-network-v1", PublicOrigin: "http://cm.example", TrustedProxies: []string{"10.0.0.1"}}
	req := gateway.CreateGatewayRequest{InstanceID: 42, UserID: 1, UID: os.Getuid(), GID: os.Getgid(), SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "old-raw-token", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "config.yaml"} {
		data, err := os.ReadFile(filepath.Join(workspace, "home", ".hermes", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old-raw-token") || !strings.Contains(string(data), managedconfig.ModelURL) {
			t.Fatalf("unsafe generated config %s", name)
		}
	}
}

func TestProtectedHermesLegacyWriterPreservesMigratedCustomSettings(t *testing.T) {
	t.Setenv(desktopWebFlag, "false")
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".hermes")
	os.MkdirAll(home, 0700)
	os.WriteFile(filepath.Join(home, ".env"), []byte("CUSTOM_KEY=third-party\nOPENAI_API_KEY=cm-local-model\n"), 0600)
	os.WriteFile(filepath.Join(home, "gateway.json"), []byte(`{"custom":"third-party"}`), 0600)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("model:\n  provider: clawmanager\nproviders:\n  private:\n    api_key: third-party\n"), 0600)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{UID: os.Getuid(), GID: os.Getgid(), SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "config.yaml", "gateway.json"} {
		data, _ := os.ReadFile(filepath.Join(home, name))
		if !strings.Contains(string(data), "third-party") {
			t.Errorf("native configuration writer erased migrated custom settings in %s", name)
		}
		if name == "config.yaml" {
			if _, err := parseDesktopWebYAML(data); err != nil {
				t.Errorf("wrote duplicate managed YAML fields: %v", err)
			}
		}
	}
}

func TestManagedCredentialMigrationLeavesUnprotectedFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	name := filepath.Join(workspace, "home/.hermes/config.yaml")
	os.MkdirAll(filepath.Dir(name), 0700)
	os.WriteFile(name, []byte("unchanged legacy custom configuration"), 0600)
	if err := NewProfile("hermes").MigrateManagedCredentials(context.Background(), gateway.Config{}, gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "unchanged legacy custom configuration" {
		t.Fatal("unprotected data was migrated")
	}
}

func TestManagedCredentialMigrationProviderAliases(t *testing.T) {
	for _, source := range []string{"yaml", "env", "third-party", "unidentified"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "instance")
			home := filepath.Join(workspace, "home/.hermes")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			secret, custom := rand.Text(), rand.Text()
			model := "model:\n  provider: clawmanager\n  api_key: " + secret + "\n"
			env := ""
			if source != "yaml" {
				endpoint := "http://backend.ns.svc/api/v1/gateway/llm"
				if source == "third-party" {
					endpoint = "https://vendor.example/v1"
				}
				model = "model:\n  provider: custom\n  base_url: " + endpoint + "\n"
				env = "OPENAI_BASE_URL=" + endpoint + "\nOPENAI_API_KEY=" + secret + "\n"
			}
			if source == "unidentified" {
				model = "model:\n  api_key: " + secret + "\n"
				env = ""
			}
			for _, key := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"} {
				env += key + "=" + secret + "\nexport " + key + "='" + secret + "'\n" + key + "=" + custom + "\n"
			}
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(model), 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{".env", ".env.bak", "..env.0123456789abcdef01234567.tmp"} {
				if err := os.WriteFile(filepath.Join(home, name), []byte(env), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
			req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
			if err := NewProfile("hermes").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{".env", ".env.bak", "..env.0123456789abcdef01234567.tmp"} {
				data, err := os.ReadFile(filepath.Join(home, name))
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"} {
					if !strings.Contains(string(data), key+"="+custom) {
						t.Errorf("independent alias lost: %s", key)
					}
				}
				if strings.Contains(string(data), secret) != (source == "third-party" || source == "unidentified") {
					t.Errorf("provider alias ownership mismatch in %s", name)
				}
			}
		})
	}
}

func TestManagedCredentialMigrationRedisTeamOverrides(t *testing.T) {
	for _, binding := range []string{"matching", "mismatch", "missing"} {
		t.Run(binding, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "instance")
			home := filepath.Join(workspace, "home/.hermes")
			os.MkdirAll(home, 0700)
			secret := rand.Text()
			raw := "platforms:\n  redis_team:\n    extra:\n      team_id: team-42\n      redis_url: redis://user:" + secret + "@old:6379/4\n      redisUrl: redis://user:" + secret + "@old:6379/5\n      team_token: " + secret + "\n      teamToken: " + secret + "\n      custom: retained\n"
			os.WriteFile(filepath.Join(home, "config.yaml"), []byte(raw), 0600)
			cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
			env := map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL, "CLAWMANAGER_TEAM_ID": "team-42", "CLAWMANAGER_TEAM_REDIS_URL": "redis://clawmanager-local-services.invalid:31283/7"}
			if binding == "mismatch" {
				env["CLAWMANAGER_TEAM_ID"] = "other-team"
			}
			if binding == "missing" {
				delete(env, "CLAWMANAGER_TEAM_ID")
			}
			err := NewProfile("hermes").MigrateManagedCredentials(context.Background(), cfg, gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: env}, workspace)
			if binding != "matching" {
				if err == nil {
					t.Fatal("unbound Team override accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
			if strings.Contains(string(data), secret) || !strings.Contains(string(data), "31283/7") || !strings.Contains(string(data), "custom: retained") {
				t.Fatal("Team credential override survived or unrelated field lost")
			}
		})
	}
}
