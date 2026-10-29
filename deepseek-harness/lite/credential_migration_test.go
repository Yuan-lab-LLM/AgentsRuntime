package deepseek

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedCredentialMigration(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "instance", "home", ".dsh")
	os.MkdirAll(home, 0700)
	user := filepath.Join(home, "custom.yaml")
	os.WriteFile(user, []byte("my_provider_key: private"), 0600)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := NewProfile("deepseek-harness").MigrateManagedCredentials(context.Background(), cfg, req, filepath.Join(root, "instance")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(user)
	if string(data) != "my_provider_key: private" {
		t.Fatal("touched user-owned DSH configuration")
	}
	req.Environment["CLAWMANAGER_LLM_API_KEY"] = "old-raw-token"
	if err := NewProfile("deepseek-harness").MigrateManagedCredentials(context.Background(), cfg, req, filepath.Join(root, "instance")); err == nil {
		t.Fatal("unsafe protected request accepted")
	}
}

func TestManagedCredentialMigrationLeavesUnprotectedFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	name := filepath.Join(workspace, "home/.dsh/custom.yaml")
	os.MkdirAll(filepath.Dir(name), 0700)
	os.WriteFile(name, []byte("unchanged legacy custom configuration"), 0600)
	if err := NewProfile("deepseek-harness").MigrateManagedCredentials(context.Background(), gateway.Config{}, gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "unchanged legacy custom configuration" {
		t.Fatal("unprotected data was migrated")
	}
}
