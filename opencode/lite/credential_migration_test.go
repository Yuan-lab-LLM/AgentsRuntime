package opencode

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
	home := filepath.Join(workspace, "home", ".opencode")
	os.MkdirAll(home, 0700)
	secret := rand.Text()
	custom := rand.Text()
	raw := fmt.Sprintf(`{"provider":{"clawmanager":{"options":{"baseURL":"http://backend.ns.svc/api/v1/gateway/llm","apiKey":%q}},"personal":{"options":{"baseURL":"https://vendor.example","apiKey":%q}}},"theme":"custom"}`, secret, custom)
	for _, name := range []string{"opencode.json", "opencode.json.bak", ".cm-0123456789abcdef01234567.tmp"} {
		os.WriteFile(filepath.Join(home, name), []byte(raw), 0640)
	}
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := NewProfile("opencode").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opencode.json", "opencode.json.bak", ".cm-0123456789abcdef01234567.tmp"} {
		data, _ := os.ReadFile(filepath.Join(home, name))
		if strings.Contains(string(data), secret) || !strings.Contains(string(data), custom) || !strings.Contains(string(data), "theme") {
			t.Fatal("migration changed unrelated provider or left old secret")
		}
	}
}

func TestProtectedOpenCodeIgnoresDeploymentGatewayToken(t *testing.T) {
	cfg := gateway.Config{NetworkSecurityProfile: "isolated-network-v1", GatewayToken: "deployment-secret"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "OPENCODE_SERVER_PASSWORD": "old-raw-token"}}
	_, password := resolveOpenCodeServerAuth(cfg, req)
	if password != "ui1_new" {
		t.Fatal("protected UI used stale credential")
	}
}

func TestManagedCredentialMigrationRejectsInvalidSchema(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".opencode")
	os.MkdirAll(home, 0700)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	os.WriteFile(filepath.Join(home, "opencode.json"), []byte(`{"provider":[]}`), 0600)
	if err := NewProfile("opencode").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err == nil {
		t.Fatal("accepted unknown managed schema")
	}
}

func TestProtectedOpenCodeWriterPreservesMigratedCustomSettings(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".opencode")
	os.MkdirAll(home, 0700)
	os.WriteFile(filepath.Join(home, "opencode.json"), []byte(`{"provider":{"personal":{"options":{"baseURL":"https://vendor.example","apiKey":"third-party"}}},"theme":"custom"}`), 0600)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{UID: os.Getuid(), GID: os.Getgid(), SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "opencode.json"))
	if !strings.Contains(string(data), "third-party") || !strings.Contains(string(data), "theme") {
		t.Fatal("native configuration writer erased migrated custom settings")
	}
}

func TestManagedCredentialMigrationLeavesUnprotectedFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	name := filepath.Join(workspace, "home/.opencode/opencode.json")
	os.MkdirAll(filepath.Dir(name), 0700)
	os.WriteFile(name, []byte("unchanged legacy custom configuration"), 0600)
	if err := NewProfile("opencode").MigrateManagedCredentials(context.Background(), gateway.Config{}, gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "unchanged legacy custom configuration" {
		t.Fatal("unprotected data was migrated")
	}
}
