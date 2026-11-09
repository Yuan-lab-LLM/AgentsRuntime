//go:build !windows

package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrepareLiteTeamSharedWorkspaceRepairsHermesWorkerParentOwnership(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-117")
	shared := filepath.Join(root, "teams", "user-1", "team-117-shared")
	workerRoot := filepath.Join(workspace, "home", ".clawmanager-team-worker")
	if err := os.MkdirAll(filepath.Join(workerRoot, ".hermes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workerRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{
		AgentType: "hermes",
		UserID:    1,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_ID":         "117",
			"CLAWMANAGER_TEAM_MEMBER_ID":  "developer",
			"CLAWMANAGER_TEAM_SHARED_DIR": shared,
		},
	}
	if err := prepareTeamWorkspace(gateway.Config{WorkspaceRoot: root}, req, workspace); err != nil {
		t.Fatalf("PrepareLiteTeamSharedWorkspace() error = %v", err)
	}
	info, err := os.Stat(workerRoot)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("worker root stat type = %T", info.Sys())
	}
	if int(stat.Uid) != req.UID || int(stat.Gid) != req.GID {
		t.Fatalf("worker root owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, req.UID, req.GID)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("worker root mode = %v, want 0750", info.Mode())
	}
}
