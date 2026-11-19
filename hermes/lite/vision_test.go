package hermes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"gopkg.in/yaml.v3"
)

func TestManagedVisionKeepsUnrelatedAuxiliaryConfiguration(t *testing.T) {
	base := "# user comment\nauxiliary:\n  compression:\n    model: keep-me\n  vision:\n    provider: openrouter\n    api_key: old-key\n    timeout: 42\n    extra_body:\n      model: unauthorized\nterminal:\n  timeout: 500\n"
	cfg := gateway.Config{LLMBaseURL: "https://gateway.example/llm", LLMVision: map[string]bool{"auto": true}}
	got, err := mergeManagedVisionYAML(base, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	aux := parsed["auxiliary"].(map[string]any)
	vision := aux["vision"].(map[string]any)
	if vision["base_url"] != cfg.LLMBaseURL || vision["model"] != "auto" || vision["provider"] != "custom" || vision["api_key"] != "" {
		t.Fatal(vision)
	}
	if vision["timeout"] != 42 || !strings.Contains(got, "keep-me") || !strings.Contains(got, "# user comment") || !strings.Contains(got, "500") || strings.Contains(got, "old-key") || strings.Contains(got, "unauthorized") {
		t.Fatal(got)
	}
	again, err := mergeManagedVisionYAML(got, cfg)
	if err != nil || again != got {
		t.Fatal("not idempotent")
	}
	legacy, err := mergeManagedVisionYAML(base, gateway.Config{})
	if err != nil || legacy != base {
		t.Fatal("legacy changed")
	}
	if _, err := mergeManagedVisionYAML("auxiliary: wrong", cfg); err == nil {
		t.Fatal("invalid mapping overwritten")
	}
}

func TestManagedVisionProtectedWriters(t *testing.T) {
	for _, desktop := range []string{"false", "true"} {
		t.Run(desktop, func(t *testing.T) {
			t.Setenv(desktopWebFlag, desktop)
			root := t.TempDir()
			workspace := filepath.Join(root, "hermes", "user-1", "instance-42")
			home := filepath.Join(workspace, "home", ".hermes")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(home, "config.yaml")
			if err := os.WriteFile(config, []byte("# user preference\nauxiliary:\n  compression:\n    model: keep-me\n  vision:\n    api_key: old-vision-key\n    timeout: 42\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", NetworkSecurityProfile: "isolated-network-v1", PublicOrigin: "http://cm.example", TrustedProxies: []string{"10.0.0.1"}}
			req := gateway.CreateGatewayRequest{InstanceID: 42, UserID: 1, UID: os.Getuid(), GID: os.Getgid(), SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL, "CLAWMANAGER_LLM_VISION": `{"auto":true}`}}
			for range 2 {
				if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(config)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := yaml.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				aux := doc["auxiliary"].(map[string]any)
				vision := aux["vision"].(map[string]any)
				if vision["base_url"] != managedconfig.ModelURL || vision["api_key"] != "" || vision["model"] != "auto" || vision["timeout"] != 42 || aux["compression"].(map[string]any)["model"] != "keep-me" {
					t.Fatalf("managed vision lost in protected writer: %s", data)
				}
			}
		})
	}
}
