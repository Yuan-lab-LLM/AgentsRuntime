package gateway

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLiteTeamGatewayCommandOnlyWrapsTeamGateway(t *testing.T) {
	command := []string{"openclaw", "gateway", "run"}
	if got := LiteTeamGatewayCommand("openclaw", command, []string{"CLAWMANAGER_TEAM_ENABLED=false"}); !reflect.DeepEqual(got, command) {
		t.Fatalf("non-Team command changed: %#v", got)
	}

	got := LiteTeamGatewayCommand("openclaw", command, []string{
		"CLAWMANAGER_TEAM_ENABLED=true",
		"CLAWMANAGER_TEAM_UMASK=0002",
	})
	wantPrefix := []string{"/bin/sh", "-c", `umask "$1"; shift; exec "$@"`, "clawmanager-team-gateway", "0002"}
	if len(got) != len(wantPrefix)+len(command) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) || !reflect.DeepEqual(got[len(wantPrefix):], command) {
		t.Fatalf("Team command = %#v", got)
	}
}

func TestLiteTeamGatewayCommandRejectsInvalidUmask(t *testing.T) {
	got := LiteTeamGatewayCommand("openclaw", []string{"gateway"}, []string{
		"CLAWMANAGER_TEAM_ENABLED=true",
		"CLAWMANAGER_TEAM_UMASK=0022; touch /tmp/escaped",
	})
	if got[4] != "0002" {
		t.Fatalf("invalid Team umask was not replaced: %#v", got)
	}
}

func TestLiteTeamBehaviorIsLimitedToSupportedRuntimes(t *testing.T) {
	for _, runtimeType := range []string{"openclaw-shell", "unknown"} {
		t.Run(runtimeType, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
			workspace := filepath.Join(root, runtimeType, "user-1", "instance-2")
			req := CreateGatewayRequest{
				AgentType:     runtimeType,
				InstanceID:    2,
				UserID:        1,
				WorkspacePath: workspace,
				Environment: map[string]string{
					"CLAWMANAGER_TEAM_ENABLED":     "true",
					"CLAWMANAGER_TEAM_CONFIG_JSON": `{ "teamId": "54" }`,
					"CLAWMANAGER_TEAM_SHARED_DIR":  shared,
				},
			}

			if _, _, ok := LiteTeamEnvironment(req, workspace); ok {
				t.Fatal("unsupported runtime received Lite Team environment")
			}
			inputEnv := []string{"CLAWMANAGER_TEAM_CONFIG_PATH=/etc/clawmanager/team/team.json"}
			if got := ApplyLiteTeamConfigEnvironment(append([]string(nil), inputEnv...), req, workspace); !reflect.DeepEqual(got, inputEnv) {
				t.Fatalf("unsupported runtime remapped Team environment: %#v", got)
			}
			if err := WriteLiteTeamConfigJSON(req, workspace); err != nil {
				t.Fatalf("WriteLiteTeamConfigJSON() error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(shared, "team.json")); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime wrote team.json: %v", err)
			}
			if _, err := PrepareWorkspace(root, runtimeType, req); err != nil {
				t.Fatalf("PrepareWorkspace() error = %v", err)
			}
			if _, err := os.Stat(shared); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime created shared Team workspace: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(workspace, "home", ".openclaw", "workspace", "team")); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime created OpenClaw Team alias: %v", err)
			}
			command := []string{"gateway", "run"}
			if got := LiteTeamGatewayCommand(runtimeType, command, []string{"CLAWMANAGER_TEAM_ENABLED=true"}); !reflect.DeepEqual(got, command) {
				t.Fatalf("unsupported runtime received Team umask wrapper: %#v", got)
			}
		})
	}
}

func TestHermesLiteTeamEnvironmentAndConfigMatchOpenClawContract(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-2")
	shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
	req := CreateGatewayRequest{
		AgentType:     "hermes",
		InstanceID:    2,
		UserID:        1,
		WorkspacePath: workspace,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":     "true",
			"CLAWMANAGER_TEAM_ID":          "54",
			"CLAWMANAGER_TEAM_MEMBER_ID":   "developer",
			"CLAWMANAGER_TEAM_CONFIG_JSON": `{"teamId":"54","memberId":"developer"}`,
			"CLAWMANAGER_TEAM_SHARED_DIR":  shared,
		},
	}

	if _, _, ok := LiteTeamEnvironment(req, workspace); !ok {
		t.Fatal("Hermes Lite Team environment was not enabled")
	}
	if _, err := PrepareWorkspace(root, "hermes", req); err != nil {
		t.Fatalf("PrepareWorkspace() error = %v", err)
	}
	if err := WriteLiteTeamConfigJSON(req, workspace); err != nil {
		t.Fatalf("WriteLiteTeamConfigJSON() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "team.json")); err != nil {
		t.Fatalf("Hermes Team config missing: %v", err)
	}
	for _, alias := range []string{
		filepath.Join(workspace, "home", ".hermes", "team"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "team"),
	} {
		target, err := os.Readlink(alias)
		if err != nil {
			t.Fatalf("read Hermes Team alias %s: %v", alias, err)
		}
		if filepath.Clean(target) != filepath.Clean(shared) {
			t.Fatalf("Hermes Team alias = %q want %q", target, shared)
		}
	}
	for _, directory := range []string{
		filepath.Join(workspace, "home", ".clawmanager-team-worker"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "runtime"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".cache", "npm"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".cache", "uv"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".config"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".local", "share"),
	} {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("Hermes Team worker directory %s missing: %v", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatalf("Hermes Team worker path %s is not a real directory", directory)
		}
	}
	readyFile := filepath.Join(
		workspace,
		"home",
		".clawmanager-team-worker",
		".hermes",
		"runtime",
		"redis-team.ready.json",
	)
	for _, stale := range []string{readyFile, readyFile + ".failed"} {
		if err := os.WriteFile(stale, []byte(`{"ready":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PrepareWorkspace(root, "hermes", req); err != nil {
		t.Fatalf("PrepareWorkspace() stale readiness cleanup error = %v", err)
	}
	for _, stale := range []string{readyFile, readyFile + ".failed"} {
		if _, err := os.Lstat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale Hermes Team startup state was not cleared: %s (%v)", stale, err)
		}
	}
}

func TestOpenClawNonTeamDoesNotCreateTeamWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	req := CreateGatewayRequest{
		AgentType:     "openclaw",
		InstanceID:    2,
		UserID:        1,
		WorkspacePath: workspace,
	}
	if _, err := PrepareWorkspace(root, "openclaw", req); err != nil {
		t.Fatalf("PrepareWorkspace() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "home", ".openclaw", "workspace", "team")); !os.IsNotExist(err) {
		t.Fatalf("non-Team OpenClaw runtime created Team alias: %v", err)
	}
}

func TestHermesLiteTeamRejectsSymlinkedWorkerHome(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-3")
	shared := filepath.Join(root, "teams", "user-1", "team-55-shared")
	workerParent := filepath.Join(workspace, "home")
	if err := os.MkdirAll(workerParent, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(workerParent, ".clawmanager-team-worker")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	req := CreateGatewayRequest{
		AgentType:     "hermes",
		InstanceID:    3,
		UserID:        1,
		WorkspacePath: workspace,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_ID":         "55",
			"CLAWMANAGER_TEAM_MEMBER_ID":  "developer",
			"CLAWMANAGER_TEAM_SHARED_DIR": shared,
		},
	}
	if _, err := PrepareWorkspace(root, "hermes", req); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("PrepareWorkspace() error = %v, want symlink rejection", err)
	}
}

func TestLiteTeamEnvironmentRemapsGlobalConfigPath(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-1", "instance-2")
	shared := filepath.Join(t.TempDir(), "teams", "user-1", "team-54-shared")
	req := CreateGatewayRequest{AgentType: "openclaw", Environment: map[string]string{
		"CLAWMANAGER_TEAM_ENABLED":     "true",
		"CLAWMANAGER_TEAM_CONFIG_JSON": `{ "teamId": "54" }`,
		"CLAWMANAGER_TEAM_CONFIG_PATH": "/etc/clawmanager/team/team.json",
		"CLAWMANAGER_TEAM_SHARED_DIR":  shared,
	}}
	configPath, sharedDir, ok := LiteTeamEnvironment(req, workspace)
	if !ok {
		t.Fatal("expected Team environment")
	}
	if sharedDir != shared {
		t.Fatalf("sharedDir = %q want %q", sharedDir, shared)
	}
	if configPath != filepath.Join(shared, "team.json") {
		t.Fatalf("configPath = %q", configPath)
	}
}

func TestWriteLiteTeamConfigRejectsEscapedPathBeforeWriting(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
	escaped := filepath.Join(root, "outside", "team.json")
	req := CreateGatewayRequest{AgentType: "openclaw", Environment: map[string]string{
		"CLAWMANAGER_TEAM_ENABLED":     "true",
		"CLAWMANAGER_TEAM_CONFIG_JSON": `{ "teamId": "54" }`,
		"CLAWMANAGER_TEAM_CONFIG_PATH": escaped,
		"CLAWMANAGER_TEAM_SHARED_DIR":  shared,
	}}
	if err := WriteLiteTeamConfigJSON(req, filepath.Join(root, "openclaw", "user-1", "instance-2")); err == nil {
		t.Fatal("expected escaped Team config path to be rejected")
	}
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("escaped Team config was written before validation: %v", err)
	}
}

func TestPrepareLiteTeamSharedWorkspaceRejectsOtherTeamPath(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	req := CreateGatewayRequest{AgentType: "openclaw", UserID: 1, Environment: map[string]string{
		"CLAWMANAGER_TEAM_ENABLED":    "true",
		"CLAWMANAGER_TEAM_ID":         "54",
		"CLAWMANAGER_TEAM_MEMBER_ID":  "pm",
		"CLAWMANAGER_TEAM_SHARED_DIR": filepath.Join(root, "teams", "user-1", "team-53-shared"),
	}}
	if err := PrepareLiteTeamSharedWorkspace(root, req, workspace); err == nil {
		t.Fatal("expected cross-Team shared path to be rejected")
	}
}

func TestOpenClawGatewayEnvIsolatesInstancePaths(t *testing.T) {
	base := []string{
		"HOME=/shared/home",
		"CLAWMANAGER_WORKSPACE_PATH=/shared/workspace",
		"CLAWMANAGER_AGENT_PERSISTENT_DIR=/shared/persistent",
	}
	tests := []struct {
		name          string
		userID        int
		instanceID    int
		workspacePath string
	}{
		{name: "gateway 123", userID: 45, instanceID: 123, workspacePath: filepath.Join("/workspaces", "openclaw", "user-45", "instance-123")},
		{name: "gateway 456", userID: 98, instanceID: 456, workspacePath: filepath.Join("/workspaces", "openclaw", "user-98", "instance-456")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := CreateGatewayRequest{
				AgentType:  "openclaw",
				UserID:     tc.userID,
				InstanceID: tc.instanceID,
				Environment: map[string]string{
					"HOME":                             "/request/home",
					"CLAWMANAGER_WORKSPACE_PATH":       "/request/workspace",
					"CLAWMANAGER_AGENT_PERSISTENT_DIR": "/request/persistent",
				},
			}
			env := OpenClawGatewayEnv(base, Config{RuntimeType: "openclaw", GatewayAuthMode: "trusted-proxy"}, req, tc.workspacePath, 20000+tc.instanceID%100)
			if got := envValue(env, "CLAWMANAGER_WORKSPACE_PATH"); got != tc.workspacePath {
				t.Fatalf("CLAWMANAGER_WORKSPACE_PATH = %q, want %q", got, tc.workspacePath)
			}
			if got, want := envValue(env, "HOME"), filepath.Join(tc.workspacePath, "home"); got != want {
				t.Fatalf("HOME = %q, want %q", got, want)
			}
			if got, want := envValue(env, "CLAWMANAGER_AGENT_PERSISTENT_DIR"), filepath.Join(tc.workspacePath, "home", ".openclaw"); got != want {
				t.Fatalf("CLAWMANAGER_AGENT_PERSISTENT_DIR = %q, want %q", got, want)
			}
		})
	}
}

func TestOpenClawGatewayEnvUsesManagedInstancePasswordForLocalTools(t *testing.T) {
	req := CreateGatewayRequest{
		AgentType:  "openclaw",
		UserID:     45,
		InstanceID: 123,
		Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "instance-123-secret",
			"OPENCLAW_GATEWAY_PASSWORD":  "unmanaged-override",
			"OPENCLAW_GATEWAY_TOKEN":     "stale-token",
		},
	}
	env := OpenClawGatewayEnv(
		[]string{"OPENCLAW_GATEWAY_PASSWORD=base-password"},
		Config{RuntimeType: "openclaw", GatewayAuthMode: "trusted-proxy"},
		req,
		filepath.Join("/workspaces", "openclaw", "user-45", "instance-123"),
		20123,
	)
	if got := envValue(env, "OPENCLAW_GATEWAY_PASSWORD"); got != "instance-123-secret" {
		t.Fatalf("OPENCLAW_GATEWAY_PASSWORD = %q, want managed instance token", got)
	}
	if got := envValue(env, "OPENCLAW_GATEWAY_TOKEN"); got != "" {
		t.Fatalf("OPENCLAW_GATEWAY_TOKEN = %q, want unset in trusted-proxy mode", got)
	}
}

func TestOpenClawGatewayEnvDoesNotChangeTokenAuthContract(t *testing.T) {
	req := CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_INSTANCE_TOKEN": "instance-secret",
	}}
	env := OpenClawGatewayEnv(
		nil,
		Config{RuntimeType: "openclaw", GatewayAuthMode: "token", GatewayToken: "gateway-token"},
		req,
		filepath.Join("/workspaces", "openclaw", "user-1", "instance-2"),
		20002,
	)
	if got := envValue(env, "OPENCLAW_GATEWAY_TOKEN"); got != "gateway-token" {
		t.Fatalf("OPENCLAW_GATEWAY_TOKEN = %q, want configured token", got)
	}
	if got := envValue(env, "OPENCLAW_GATEWAY_PASSWORD"); got != "" {
		t.Fatalf("OPENCLAW_GATEWAY_PASSWORD = %q, want unset for token auth", got)
	}
}
