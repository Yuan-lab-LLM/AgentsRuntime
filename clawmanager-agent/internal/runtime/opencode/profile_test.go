package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

func TestProfileGatewayCommand(t *testing.T) {
	p := NewProfile("opencode")
	cmd := p.GatewayCommand("token")
	if len(cmd) != 1 || cmd[0] != "start-opencode-web" {
		t.Fatalf("GatewayCommand = %#v", cmd)
	}
}

func TestGatewayEnvSetsOpenCodeAuthAndHome(t *testing.T) {
	p := NewProfile("opencode")
	req := gateway.CreateGatewayRequest{
		InstanceID: 7,
		UserID:     3,
		Generation: 2,
		Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "igt_secret",
			"OPENCODE_SERVER_USERNAME":   "opencode",
		},
	}
	env := p.GatewayEnv(nil, gateway.Config{RuntimeType: "opencode"}, req, "/workspaces/opencode/user-3/instance-7", 20042)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HOME=/workspaces/opencode/user-3/instance-7/home",
		"OPENCODE_CONFIG_DIR=/workspaces/opencode/user-3/instance-7/home/.opencode",
		"OPENCODE_SERVER_PASSWORD=igt_secret",
		"OPENCODE_SERVER_USERNAME=opencode",
		"PORT=20042",
		"HOST=0.0.0.0",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q in:\n%s", want, joined)
		}
	}
}

func TestWriteGatewayConfigCreatesLockedProvider(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "opencode", "user-1", "instance-9")
	home := filepath.Join(workspace, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{
		UID: 0,
		GID: 0,
		Environment: map[string]string{
			"CLAWMANAGER_LLM_BASE_URL": "http://gateway.example/v1",
			"CLAWMANAGER_LLM_API_KEY":  "igt_key",
			"CLAWMANAGER_LLM_MODEL":    "auto",
		},
	}
	if err := WriteGatewayConfig(gateway.Config{}, req, workspace); err != nil {
		t.Fatalf("WriteGatewayConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`"clawmanager"`,
		`@ai-sdk/openai-compatible`,
		`http://gateway.example/v1`,
		`{env:CLAWMANAGER_LLM_API_KEY}`,
		`"enabled_providers"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("config missing %q in %s", want, body)
		}
	}
}

func TestWriteGatewayConfigRequiresBaseURL(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	_ = os.MkdirAll(filepath.Join(workspace, "home"), 0o750)
	err := WriteGatewayConfig(gateway.Config{}, gateway.CreateGatewayRequest{
		Environment: map[string]string{"CLAWMANAGER_LLM_API_KEY": "x"},
	}, workspace)
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("expected base URL error, got %v", err)
	}
}

func TestDefaults(t *testing.T) {
	d := NewProfile("opencode").Defaults()
	if d.WorkspaceRoot != "/workspaces" {
		t.Fatalf("WorkspaceRoot = %q", d.WorkspaceRoot)
	}
	if d.GatewayStartupTimeout < time.Second {
		t.Fatalf("unexpected timeout %s", d.GatewayStartupTimeout)
	}
}
