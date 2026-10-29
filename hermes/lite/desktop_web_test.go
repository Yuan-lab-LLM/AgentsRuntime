package hermes

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func strictTestRequest(root string) gateway.CreateGatewayRequest {
	return gateway.CreateGatewayRequest{InstanceID: 42, UserID: 7, Generation: 1, UID: 1000, GID: 1000, AgentType: "hermes", GatewayPort: 20000,
		WorkspacePath: filepath.Join(root, "hermes", "user-7", "instance-42"), Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "managed-auth", "CLAWMANAGER_LLM_API_KEY": "instance-model-key", "CLAWMANAGER_LLM_BASE_URL": "http://gateway.ns.svc.cluster.local:9001/v1",
		}}
}
func TestDesktopWebEnvironmentWhitelistAndAuthoritativeIdentity(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	p := NewProfile("hermes")
	root := t.TempDir()
	req := strictTestRequest(root)
	req.Environment[desktopWebFlag] = "false"
	req.Environment["HOME"] = "/root"
	req.Environment["PORT"] = "3000"
	req.Environment["PATH"] = "/tmp/attacker"
	req.Environment["NODE_OPTIONS"] = "--require /tmp/inject.js"
	req.Environment["RUNTIME_AGENT_CONTROL_TOKEN"] = "request-control-secret"
	req.Environment["UNDEFINED_ENV"] = "ignored"
	req.Environment["CLAWMANAGER_HERMES_BACKEND_MODE"] = "dashboard"
	cfg := gateway.Config{RuntimeType: "hermes", PublicOrigin: "https://manager.example", TrustedProxies: []string{"10.1.2.0/24"}, LLMAPIKey: "pod-llm-secret", GatewayToken: "pod-gateway-secret"}
	env := p.GatewayEnv([]string{"PATH=/usr/bin:/bin", "RUNTIME_AGENT_REPORT_TOKEN=report-secret", "ANTHROPIC_API_KEY=pod-provider-secret", "HERMES_DASHBOARD_SESSION_TOKEN=pod-session-secret", "PYTHONPATH=/tmp/inject"}, cfg, req, req.WorkspacePath, 20000)
	for _, key := range []string{"RUNTIME_AGENT_REPORT_TOKEN", "RUNTIME_AGENT_CONTROL_TOKEN", "ANTHROPIC_API_KEY", "HERMES_DASHBOARD_SESSION_TOKEN", "PYTHONPATH", "NODE_OPTIONS", "UNDEFINED_ENV"} {
		if envValue(env, key) != "" {
			t.Errorf("forwarded %s", key)
		}
	}
	for key, want := range map[string]string{"PATH": "/usr/bin:/bin", "HOME": filepath.Join(req.WorkspacePath, "home"), "PORT": "20000", desktopWebFlag: "true", "CLAWMANAGER_HERMES_BACKEND_MODE": "serve", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "managed-auth", "OPENAI_API_KEY": "instance-model-key", "CLAWMANAGER_CONTROL_UI_ORIGIN": "https://manager.example", "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "10.1.2.0/24"} {
		if envValue(env, key) != want {
			t.Errorf("wrong value for %s", key)
		}
	}
	if got := p.GatewayCommand(""); len(got) != 1 || got[0] != "start-hermes-lite-runtime" {
		t.Fatal(got)
	}
}
func TestDesktopWebRejectsUnsupportedBackendBeforeWorkspaceMutation(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	for _, mode := range []string{"dashboard", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", mode)
			root := t.TempDir()
			req := strictTestRequest(root)
			err := NewProfile("hermes").PrepareWorkspace(gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", GatewayPortStart: 20000, GatewayPortEnd: 20299}, req, req.WorkspacePath)
			if err == nil || !strings.HasPrefix(err.Error(), "unsupported_hermes_protocol") {
				t.Fatal(err)
			}
			if _, err = os.Stat(req.WorkspacePath); !os.IsNotExist(err) {
				t.Fatal("unsupported mode created workspace")
			}
		})
	}
}

func TestDesktopWebTeamUsesExistingTeamContractAlongsideDashboard(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", "serve")
	root := t.TempDir()
	req := strictTestRequest(root)
	// Workspace preparation needs an owner the test process can assign.
	// Root keeps the positive instance identity required by Desktop Web.
	if os.Geteuid() != 0 {
		req.UID, req.GID = os.Geteuid(), os.Getegid()
	}
	req.Environment["CLAWMANAGER_TEAM_ENABLED"] = "true"
	req.Environment["CLAWMANAGER_TEAM_ID"] = "42"
	req.Environment["CLAWMANAGER_TEAM_MEMBER_ID"] = "leader"
	req.Environment["CLAWMANAGER_TEAM_ROLE"] = "leader"
	req.Environment["CLAWMANAGER_TEAM_REDIS_URL"] = "redis://redis.example:6379/0"
	req.Environment["CLAWMANAGER_TEAM_SHARED_DIR"] = "/team"
	req.Environment["CLAWMANAGER_TEAM_TOKEN"] = "team-secret"
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", GatewayPortStart: 20000, GatewayPortEnd: 20299}
	if err := NewProfile("hermes").PrepareWorkspace(cfg, req, req.WorkspacePath); err != nil {
		t.Fatalf("Team Desktop Web workspace rejected: %v", err)
	}
	env := NewProfile("hermes").GatewayEnv(nil, cfg, req, req.WorkspacePath, 20000)
	for key, want := range map[string]string{
		"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_ID": "42",
		"CLAWMANAGER_TEAM_MEMBER_ID": "leader", "CLAWMANAGER_TEAM_ROLE": "leader",
		"CLAWMANAGER_TEAM_REDIS_URL": "redis://redis.example:6379/0", "CLAWMANAGER_TEAM_TOKEN": "team-secret",
		"CLAWMANAGER_TEAM_SHARED_DIR":            filepath.Join(req.WorkspacePath, "team"),
		"HERMES_TEAM_WORKER_HOME":                filepath.Join(req.WorkspacePath, "home", ".clawmanager-team-worker"),
		"CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "true", "HERMES_ACCEPT_HOOKS": "1",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestDesktopWebTeamConfigAloneEnablesExistingTeamContract(t *testing.T) {
	req := gateway.CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_TEAM_CONFIG_JSON": `{"teamId":"42","memberId":"worker"}`,
		"CLAWMANAGER_TEAM_ID":          "42",
		"CLAWMANAGER_TEAM_MEMBER_ID":   "worker",
		"CLAWMANAGER_TEAM_REDIS_URL":   "redis://redis.example:6379/0",
	}}
	if !desktopWebTeamRequest(req) {
		t.Fatal("Team config JSON did not select the established Team contract")
	}
	env := desktopWebTeamEnvironment(nil, req, "/workspaces/hermes/user-1/instance-2")
	if got := envValue(env, "CLAWMANAGER_TEAM_ENABLED"); got != "true" {
		t.Fatalf("CLAWMANAGER_TEAM_ENABLED = %q, want true", got)
	}
}
func TestDesktopWebCannotUsePodAuthOrLLMCredentials(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	req := strictTestRequest(t.TempDir())
	delete(req.Environment, "CLAWMANAGER_INSTANCE_TOKEN")
	delete(req.Environment, "CLAWMANAGER_LLM_API_KEY")
	env := NewProfile("hermes").GatewayEnv([]string{"OPENAI_API_KEY=pod-key", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=pod-pass"}, gateway.Config{LLMAPIKey: "pod-key", GatewayToken: "pod-pass"}, req, req.WorkspacePath, 20000)
	if envValue(env, "OPENAI_API_KEY") != "" || envValue(env, "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD") != "" {
		t.Fatal("inherited pod credentials")
	}
	if _, _, err := dashboardCredentials(req); err == nil {
		t.Fatal("missing auth accepted")
	}
}
func TestDesktopWebWorkspaceRejectsCrossInstanceHomeSymlink(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	root := t.TempDir()
	req := strictTestRequest(root)
	outside := t.TempDir()
	if err := os.MkdirAll(req.WorkspacePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(req.WorkspacePath, "home")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", GatewayPortStart: 20000, GatewayPortEnd: 20299}
	if err := NewProfile("hermes").PrepareWorkspace(cfg, req, req.WorkspacePath); err == nil {
		t.Fatal("cross-instance home accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("escaped workspace wrote into other home")
	}
}

func TestDesktopWebRejectsCredentialIntegerTruncationBeforePreparation(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("large positive identities cannot be represented on this platform")
	}
	t.Setenv(desktopWebFlag, "true")
	t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", "serve")
	for _, raw := range []uint64{1<<32 - 1, 1 << 32, 1<<32 + 10001} {
		for _, field := range []string{"uid", "gid"} {
			t.Run(field+"-"+strconv.FormatUint(raw, 10), func(t *testing.T) {
				root := t.TempDir()
				req := strictTestRequest(root)
				if field == "uid" {
					req.UID = int(raw)
				} else {
					req.GID = int(raw)
				}
				cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", GatewayPortStart: 20000, GatewayPortEnd: 20299}
				err := NewProfile("hermes").PrepareWorkspace(cfg, req, req.WorkspacePath)
				if err == nil || !strings.HasPrefix(err.Error(), "invalid_gateway_identity") {
					t.Fatalf("invalid credential accepted: %v", err)
				}
				if _, err := os.Stat(req.WorkspacePath); !os.IsNotExist(err) {
					t.Fatal("invalid credential mutated workspace")
				}
			})
		}
	}
}
