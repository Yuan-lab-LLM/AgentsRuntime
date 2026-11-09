package openclaw

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
	"time"
)

func TestManagedCredentialMigration(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(home, 0700)
	secret := rand.Text()
	custom := rand.Text()
	raw := fmt.Sprintf(`{"models":{"providers":{"auto":{"baseUrl":"http://backend.ns.svc/api/v1/gateway/llm","apiKey":%q},"personal":{"baseUrl":"https://vendor.example/v1","apiKey":%q}}},"gateway":{"auth":{"token":%q,"password":%q}},"env":{"CLAWMANAGER_TEAM_TOKEN":%q},"channels":{"redis-team":{"accounts":{"default":{"redisUrl":"redis://old:%s@redis.ns.svc:6379/4","teamToken":%q}}}},"browser":{"profiles":{"interactive":{"cdpUrl":"http://u:%s@clawbrowser-42.ns.svc.cluster.local:9222"},"personal":{"cdpUrl":"https://user:third-party@example.com"}}}}`, secret, custom, secret, secret, secret, secret, secret, secret)
	names := []string{"openclaw.json", "openclaw.json.bak", "openclaw.json.bak-browser-worker", "openclaw.json.browser-worker-state", ".browser-worker-probe", ".cm-0123456789abcdef01234567.tmp"}
	for _, name := range names {
		os.WriteFile(filepath.Join(home, name), []byte(raw), 0600)
	}
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{InstanceID: 42, SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, _ := os.ReadFile(filepath.Join(home, name))
		if strings.Contains(string(data), secret) || !strings.Contains(string(data), custom) || !strings.Contains(string(data), "third-party") || !strings.Contains(string(data), "31283/4") || !strings.Contains(string(data), managedconfig.ServicesURL) {
			t.Fatalf("migration did not preserve boundaries for %s: %s", name, data)
		}
	}
}

func TestManagedCredentialMigrationRejectsInvalidSchema(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(home, 0700)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	for _, raw := range []string{`{"models":false}`, `{"models":{"providers":[]}}`, `{"gateway":{"auth":"old-secret"}}`, `{"channels":{"redis-team":{"accounts":[]}}}`} {
		os.WriteFile(filepath.Join(home, "openclaw.json"), []byte(raw), 0600)
		if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err == nil {
			t.Fatalf("accepted unknown managed schema %s", raw)
		}
	}
}

type migrationPreparationFixture struct {
	Profile
	observed chan error
}

func (p migrationPreparationFixture) PrepareWorkspace(_ gateway.Config, _ gateway.CreateGatewayRequest, workspace string) error {
	data, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err == nil && (!strings.Contains(string(data), managedconfig.ModelKey) || strings.Contains(string(data), "old-cm-token")) {
		err = fmt.Errorf("native hook saw unmigrated credentials")
	}
	p.observed <- err
	return fmt.Errorf("fixture finished before native execution")
}

type migrationPreparationStarter struct{}

func (migrationPreparationStarter) PrepareGateway(context.Context, gateway.CreateGatewayRequest, int) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}
func (migrationPreparationStarter) StartGateway(context.Context, gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	return gateway.ManagedProcess{}, fmt.Errorf("native exec not expected")
}
func TestManagedCredentialMigrationBeforeNativePreparation(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-42")
	home := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(home, 0700)
	os.WriteFile(filepath.Join(home, "openclaw.json"), []byte(`{"models":{"providers":{"auto":{"baseUrl":"http://backend.ns.svc/api/v1/gateway/llm","apiKey":"old-cm-token"}}}}`), 0600)
	observed := make(chan error, 1)
	cfg := gateway.Config{RuntimeType: "openclaw", Runtime: migrationPreparationFixture{NewProfile("openclaw"), observed}, WorkspaceRoot: root, IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3, Capacity: 1}
	req := gateway.CreateGatewayRequest{InstanceID: 42, UserID: 1, Generation: 1, AgentType: "openclaw", UID: os.Getuid(), GID: os.Getgid(), GatewayPort: 20000, WorkspacePath: workspace, SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	manager := gateway.NewGatewayManager(cfg, migrationPreparationStarter{}, gateway.NewPortAllocator(func(int) bool { return false }))
	defer manager.Shutdown(context.Background())
	if _, err := manager.CreateGateway(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native preparation was not reached")
	}
}

func TestManagedCredentialMigrationGeneratedModelState(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(home, 0700)
	secret := rand.Text()
	custom := rand.Text()
	customDir := filepath.Join(workspace, "home", "custom-agent")
	primary := fmt.Sprintf(`{"agents":{"list":[{"id":"custom","agentDir":%q}]}}`, customDir)
	os.WriteFile(filepath.Join(home, "openclaw.json"), []byte(primary), 0600)
	paths := []string{filepath.Join(home, "agents", "main", "agent"), filepath.Join(home, "agents", "worker", "agent"), customDir}
	for _, dir := range paths {
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "models.json"), []byte(fmt.Sprintf(`{"providers":{"auto":{"baseUrl":"http://backend.ns.svc/api/v1/gateway/llm","apiKey":%q},"personal":{"baseUrl":"https://vendor.example","apiKey":%q}}}`, secret, custom)), 0600)
	}
	for _, dir := range paths {
		data, _ := os.ReadFile(filepath.Join(dir, "models.json"))
		os.WriteFile(filepath.Join(dir, ".fs-safe-01234567-89ab-4cde-8fab-0123456789ab.tmp"), data, 0600)
	}
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, dir := range paths {
		data, _ := os.ReadFile(filepath.Join(dir, "models.json"))
		temp, _ := os.ReadFile(filepath.Join(dir, ".fs-safe-01234567-89ab-4cde-8fab-0123456789ab.tmp"))
		data = append(data, temp...)
		if strings.Contains(string(data), secret) || !strings.Contains(string(data), custom) {
			t.Fatal("generated model state still contains old CM credential or lost custom provider")
		}
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(home, "openclaw.json"), []byte(fmt.Sprintf(`{"agents":{"list":[{"agentDir":%q}]}}`, outside)), 0600)
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err == nil {
		t.Fatal("accepted outside native model state directory")
	}
}

func TestProtectedOpenClawPinsNativeStateDirectory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", NetworkSecurityProfile: "isolated-network-v1", GatewayToken: "deployment-secret"}
	req := gateway.CreateGatewayRequest{SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	env := strings.Join(OpenClawGatewayEnv(nil, cfg, req, workspace, 20000), "\n")
	if !strings.Contains(env, "OPENCLAW_STATE_DIR="+filepath.Join(workspace, "home", ".openclaw")) {
		t.Fatal("native state root not pinned")
	}
	if strings.Contains(env, "deployment-secret") {
		t.Fatal("protected native received deployment credential")
	}
	req.Environment["OPENCLAW_AGENT_DIR"] = t.TempDir()
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err == nil {
		t.Fatal("outside native agent directory accepted")
	}
	delete(req.Environment, "OPENCLAW_AGENT_DIR")
	req.Environment["OPENCLAW_STATE_DIR"] = t.TempDir()
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err == nil {
		t.Fatal("unmigrated alternate state root accepted")
	}
}

func TestManagedCredentialMigrationLeavesUnprotectedFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	name := filepath.Join(workspace, "home/.openclaw/openclaw.json")
	os.MkdirAll(filepath.Dir(name), 0700)
	os.WriteFile(name, []byte("unchanged legacy custom configuration"), 0600)
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), gateway.Config{}, gateway.CreateGatewayRequest{}, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "unchanged legacy custom configuration" {
		t.Fatal("unprotected data was migrated")
	}
}

func TestManagedCredentialMigrationBrowserURLArrays(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instance")
	home := filepath.Join(workspace, "home/.openclaw")
	os.MkdirAll(home, 0700)
	raw := `{"urls":["http://user:old-secret@clawbrowser-42.ns.svc:9222",["ws://clawbrowser-42.ns.svc.cluster.local:9222/path"],"https://third-party.example:9222","http://clawbrowser-43.ns.svc:9222"]}`
	os.WriteFile(filepath.Join(home, "openclaw.json.browser-worker-state"), []byte(raw), 0600)
	cfg := gateway.Config{WorkspaceRoot: root, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{InstanceID: 42, SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_new", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL}}
	if err := NewProfile("openclaw").MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "openclaw.json.browser-worker-state"))
	if strings.Contains(string(data), "clawbrowser-42") || strings.Contains(string(data), "old-secret") || strings.Count(string(data), managedconfig.ServicesURL) != 2 || !strings.Contains(string(data), "third-party.example") || !strings.Contains(string(data), "clawbrowser-43") {
		t.Fatal("array worker URL ownership migration failed")
	}
}
