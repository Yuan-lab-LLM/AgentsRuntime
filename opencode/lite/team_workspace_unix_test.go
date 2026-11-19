//go:build !windows

package opencode

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeTeamProjectUsesItsOwnSharedWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "opencode", "user-3", "instance-7")
	shared := filepath.Join(root, "teams", "user-3", "team-12-shared")
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 3, UID: os.Getuid(), GID: os.Getgid(), WorkspacePath: workspace, ProjectRelativePath: "starter", Environment: map[string]string{
		"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_ID": "12", "CLAWMANAGER_TEAM_MEMBER_ID": "writer", "CLAWMANAGER_TEAM_SHARED_DIR": shared, "CLAWMANAGER_TEAM_CONFIG_JSON": `{"members":[{"memberId":"writer"}]}`,
	}}
	profile := NewProfile("opencode")
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "opencode"}
	for i := 0; i < 2; i++ {
		if err := profile.PrepareWorkspace(cfg, req, workspace); err != nil {
			t.Fatal(err)
		}
	}
	target, err := os.Readlink(filepath.Join(workspace, "starter", "team"))
	if err != nil || target != shared {
		t.Fatalf("alias=%s error=%v", target, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "team.json")); err != nil {
		t.Fatal(err)
	}
	req.Environment["CLAWMANAGER_TEAM_SHARED_DIR"] = filepath.Join(root, "teams", "user-3", "team-13-shared")
	if err := profile.PrepareWorkspace(cfg, req, workspace); err == nil {
		t.Fatal("another Team workspace accepted")
	}
}
