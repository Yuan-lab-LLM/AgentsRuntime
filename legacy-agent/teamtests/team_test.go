package teamtests

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-legacy-agent/profiles"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLiteTeamBehaviorIsLimitedToSupportedRuntimes(t *testing.T) {
	for _, runtimeType := range []string{"openclaw-shell", "unknown"} {
		t.Run(runtimeType, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
			workspace := filepath.Join(root, runtimeType, "user-1", "instance-2")
			req := gateway.CreateGatewayRequest{
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

			t.Setenv("CLAWMANAGER_RUNTIME_TYPE", runtimeType)
			t.Setenv("RUNTIME_GATEWAY_COMMAND", "gateway run")
			profile, err := profiles.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: runtimeType, Runtime: profile}
			inputEnv := []string{"CLAWMANAGER_TEAM_CONFIG_PATH=/etc/clawmanager/team/team.json"}
			if got := profile.GatewayEnv(append([]string(nil), inputEnv...), cfg, req, workspace, 20000); gateway.EnvironmentValue(got, "CLAWMANAGER_TEAM_CONFIG_PATH") != inputEnv[0][len("CLAWMANAGER_TEAM_CONFIG_PATH="):] {
				t.Fatalf("unsupported runtime remapped Team environment: %#v", got)
			}

			if _, err := os.Stat(filepath.Join(shared, "team.json")); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime wrote team.json: %v", err)
			}
			if err := profile.PrepareWorkspace(cfg, req, workspace); err != nil {
				t.Fatalf("PrepareWorkspace() error = %v", err)
			}
			if _, err := os.Stat(shared); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime created shared Team workspace: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(workspace, "home", ".openclaw", "workspace", "team")); !os.IsNotExist(err) {
				t.Fatalf("unsupported runtime created OpenClaw Team alias: %v", err)
			}
			command := []string{"gateway", "run"}
			if got := gateway.RuntimeGatewayCommand(cfg, command, []string{"CLAWMANAGER_TEAM_ENABLED=true"}); !reflect.DeepEqual(got, command) {
				t.Fatalf("unsupported runtime received Team umask wrapper: %#v", got)
			}
		})
	}
}
