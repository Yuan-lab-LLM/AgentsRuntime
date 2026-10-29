package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestProfileGatewayCommand(t *testing.T) {
	p := NewProfile("opencode")
	cmd := p.GatewayCommand("token")
	if len(cmd) != 1 || cmd[0] != "start-opencode-web" {
		t.Fatalf("GatewayCommand = %#v", cmd)
	}
}

func TestGatewayEnvRequestsEarlierCollectionInsideSlot(t *testing.T) {
	req := gateway.CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_INSTANCE_TOKEN": "test-token",
		"BUN_JSC_forceRAMSize":       "68719476736",
	}}
	env := NewProfile("opencode").GatewayEnv(nil, gateway.Config{}, req, t.TempDir(), 20000)
	for _, item := range env {
		if strings.HasPrefix(item, "BUN_JSC_forceRAMSize=") {
			if item != "BUN_JSC_forceRAMSize=536870912" {
				t.Fatalf("collection RAM heuristic = %q", item)
			}
			return
		}
	}
	t.Fatal("OpenCode must request earlier collection inside a memory-limited slot")
}

func TestGatewayEnvAlignsOpenCodeHomeWithWorkspaceRoot(t *testing.T) {
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
		"HOME=/workspaces/opencode/user-3/instance-7",
		"CLAWMANAGER_WORKSPACE_PATH=/workspaces/opencode/user-3/instance-7",
		"CLAWMANAGER_PROJECT_PATH=/workspaces/opencode/user-3/instance-7",
		"CLAWMANAGER_DEFAULT_PROJECT_PATH=/workspaces/opencode/user-3/instance-7",
		"OPENCODE_CONFIG_DIR=/workspaces/opencode/user-3/instance-7/home/.opencode",
		"XDG_CONFIG_HOME=/workspaces/opencode/user-3/instance-7/home/.config",
		"XDG_CACHE_HOME=/workspaces/opencode/user-3/instance-7/home/.cache",
		"XDG_DATA_HOME=/workspaces/opencode/user-3/instance-7/home/.local/share",
		"XDG_STATE_HOME=/workspaces/opencode/user-3/instance-7/home/.local/state",
		"OPENCODE_DISABLE_FFF=1",
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

func TestGatewayEnvUsesValidatedTypedDefaultProject(t *testing.T) {
	workspace := t.TempDir()
	starter := filepath.Join(workspace, "starter")
	if err := os.Mkdir(starter, 0o750); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{
		InstanceID:          7,
		ProjectRelativePath: "starter",
		Environment: map[string]string{
			"CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH": "../other-instance",
			"CLAWMANAGER_DEFAULT_PROJECT_PATH":          filepath.Join(workspace, "missing"),
		},
	}
	env := NewProfile("opencode").GatewayEnv(nil, gateway.Config{RuntimeType: "opencode"}, req, workspace, 20042)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"CLAWMANAGER_PROJECT_PATH=" + starter,
		"CLAWMANAGER_DEFAULT_PROJECT_PATH=" + starter,
		"CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH=starter",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q in:\n%s", want, joined)
		}
	}
}

func TestResolveOpenCodeProjectPathSupportsCompatibilityInputs(t *testing.T) {
	workspace := t.TempDir()
	starter := filepath.Join(workspace, "starter")
	if err := os.Mkdir(starter, 0o750); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		req  gateway.CreateGatewayRequest
	}{
		{
			name: "relative environment",
			req: gateway.CreateGatewayRequest{Environment: map[string]string{
				"CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH": "starter",
			}},
		},
		{
			name: "absolute environment",
			req: gateway.CreateGatewayRequest{Environment: map[string]string{
				"CLAWMANAGER_DEFAULT_PROJECT_PATH": starter,
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, relative, reason := resolveOpenCodeProjectPath(tc.req, workspace)
			if reason != "" || got != starter || relative != "starter" {
				t.Fatalf("resolveOpenCodeProjectPath() = (%q, %q, %q), want (%q, starter, empty)", got, relative, reason, starter)
			}
		})
	}
}

func TestResolveOpenCodeProjectPathKeepsExistingLegacyProject(t *testing.T) {
	workspace := t.TempDir()
	legacyProject := filepath.Join(workspace, "project")
	if err := os.Mkdir(legacyProject, 0o750); err != nil {
		t.Fatal(err)
	}
	got, relative, reason := resolveOpenCodeProjectPath(gateway.CreateGatewayRequest{}, workspace)
	if got != legacyProject || relative != "" || reason != "" {
		t.Fatalf("resolveOpenCodeProjectPath() = (%q, %q, %q), want legacy project", got, relative, reason)
	}
}

func TestResolveOpenCodeProjectPathFallsBackWithoutMutatingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(filePath, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(workspace, "missing")
	tests := []struct {
		name string
		req  gateway.CreateGatewayRequest
	}{
		{name: "legacy request", req: gateway.CreateGatewayRequest{}},
		{name: "parent escape", req: gateway.CreateGatewayRequest{ProjectRelativePath: "../instance-8/starter"}},
		{name: "absolute typed path", req: gateway.CreateGatewayRequest{ProjectRelativePath: filepath.Join(workspace, "starter")}},
		{name: "missing directory", req: gateway.CreateGatewayRequest{ProjectRelativePath: "missing"}},
		{name: "file target", req: gateway.CreateGatewayRequest{ProjectRelativePath: "file.txt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, relative, _ := resolveOpenCodeProjectPath(tc.req, workspace)
			if got != workspace || relative != "" {
				t.Fatalf("resolveOpenCodeProjectPath() = (%q, %q), want workspace fallback", got, relative)
			}
		})
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("resolver created missing project directory: %v", err)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("resolver mutated existing file: %q, %v", raw, err)
	}
}

func TestResolveOpenCodeProjectPathRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(workspace, "starter")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	got, relative, reason := resolveOpenCodeProjectPath(gateway.CreateGatewayRequest{ProjectRelativePath: "starter"}, workspace)
	if got != workspace || relative != "" || !strings.Contains(reason, "symbolic link") {
		t.Fatalf("resolveOpenCodeProjectPath() = (%q, %q, %q), want symlink fallback", got, relative, reason)
	}
}

func TestPrepareWorkspaceCreatesTypedDefaultProjectIdempotently(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "opencode", "user-3", "instance-7")
	req := gateway.CreateGatewayRequest{
		InstanceID:          7,
		UserID:              3,
		WorkspacePath:       workspace,
		ProjectRelativePath: "starter",
	}
	profile := NewProfile("opencode")
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "opencode"}

	for attempt := 0; attempt < 2; attempt++ {
		if err := profile.PrepareWorkspace(cfg, req, workspace); err != nil {
			t.Fatalf("PrepareWorkspace attempt %d: %v", attempt+1, err)
		}
	}
	info, err := os.Stat(filepath.Join(workspace, "starter"))
	if err != nil || !info.IsDir() {
		t.Fatalf("starter directory was not created: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace, "starter"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("starter must be empty, entries=%v err=%v", entries, err)
	}
}

func TestPrepareWorkspaceRejectsExistingDefaultProjectFile(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "opencode", "user-3", "instance-7")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "starter"), []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{
		InstanceID:          7,
		UserID:              3,
		WorkspacePath:       workspace,
		ProjectRelativePath: "starter",
	}
	err := NewProfile("opencode").PrepareWorkspace(
		gateway.Config{WorkspaceRoot: root, RuntimeType: "opencode"},
		req,
		workspace,
	)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected existing file rejection, got %v", err)
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

func TestWriteGatewayConfigGroupsModelsByConfiguredProvider(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "opencode", "user-1", "instance-10")
	if err := os.MkdirAll(filepath.Join(workspace, "home"), 0o750); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{
		UID: os.Getuid(),
		GID: os.Getgid(),
		Environment: map[string]string{
			"CLAWMANAGER_LLM_BASE_URL":        "http://gateway.example/v1",
			"CLAWMANAGER_LLM_API_KEY":         "igt_key",
			"CLAWMANAGER_LLM_MODEL":           `["auto","deepseek","deepseek-v4-pro","yuan-embedding-1.0"]`,
			"CLAWMANAGER_LLM_PROVIDER_MODELS": `["auto/auto","deepseek/deepseek","deepseek/deepseek-v4-pro","modellist/yuan-embedding-1.0"]`,
		},
	}
	if err := WriteGatewayConfig(gateway.Config{}, req, workspace); err != nil {
		t.Fatalf("WriteGatewayConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "home", ".opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "auto/auto" {
		t.Fatalf("model = %#v, want auto/auto", doc["model"])
	}
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider = %#v", doc["provider"])
	}
	for _, providerID := range []string{"auto", "deepseek", "modellist"} {
		if _, exists := providers[providerID]; !exists {
			t.Fatalf("provider %q missing in %#v", providerID, providers)
		}
	}
	if _, exists := providers[clawmanagerProviderID]; exists {
		t.Fatalf("legacy clawmanager provider retained in %#v", providers)
	}
	deepseekProvider := providers["deepseek"].(map[string]any)
	deepseekModels := deepseekProvider["models"].(map[string]any)
	if _, exists := deepseekModels["deepseek-v4-pro"]; !exists {
		t.Fatalf("deepseek models = %#v", deepseekModels)
	}
	disabled, ok := doc["disabled_providers"].([]any)
	if !ok {
		t.Fatalf("disabled_providers = %#v", doc["disabled_providers"])
	}
	for _, providerID := range disabled {
		if providerID == "deepseek" {
			t.Fatalf("configured provider deepseek remained disabled: %#v", disabled)
		}
	}
}

func TestLoadLLMSettingsFromEnvUsesOpenAIModelFallback(t *testing.T) {
	for _, key := range []string{
		"CLAWMANAGER_LLM_PROVIDER_MODELS",
		"CLAWMANAGER_LLM_MODEL",
		"CLAWMANAGER_LLM_API_KEY",
		"CLAWMANAGER_INSTANCE_TOKEN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "http://gateway.example/v1")
	t.Setenv("OPENAI_API_KEY", "openai-token")
	t.Setenv("OPENAI_MODEL", "gpt-5.5")

	settings, err := LoadLLMSettingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RenderConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"model": "clawmanager/auto"`, `"gpt-5.5"`, `{env:OPENAI_API_KEY}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, text)
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
