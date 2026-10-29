//go:build !windows

package gateway

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrepareLiteTeamSharedWorkspaceRepairsPermissionsAndCreatesInstanceAlias(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
	nested := filepath.Join(shared, "artifacts", "calculator")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "index.html")
	if err := os.WriteFile(file, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownerBeforeInfo, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}
	ownerBefore, ok := ownerBeforeInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("nested stat type before preparation = %T", ownerBeforeInfo.Sys())
	}
	ownerUIDBefore := ownerBefore.Uid
	req := CreateGatewayRequest{
		AgentType: "openclaw",
		UserID:    1,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_ID":         "54",
			"CLAWMANAGER_TEAM_MEMBER_ID":  "designer",
			"CLAWMANAGER_TEAM_SHARED_DIR": shared,
		},
	}
	if err := PrepareLiteTeamSharedWorkspace(root, req, workspace, filepath.Join(workspace, "home", ".openclaw", "workspace")); err != nil {
		t.Fatalf("PrepareLiteTeamSharedWorkspace() error = %v", err)
	}
	dirInfo, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o020 == 0 || dirInfo.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("nested directory mode = %v, want group-write and setgid", dirInfo.Mode())
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("nested stat type after preparation = %T", dirInfo.Sys())
	}
	if dirStat.Uid != ownerUIDBefore {
		t.Fatalf("nested directory uid changed from %d to %d", ownerUIDBefore, dirStat.Uid)
	}
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm()&0o020 == 0 {
		t.Fatalf("file mode = %v, want group-write", fileInfo.Mode())
	}
	alias := filepath.Join(workspace, "home", ".openclaw", "workspace", "team")
	target, err := os.Readlink(alias)
	if err != nil {
		t.Fatalf("read Team alias: %v", err)
	}
	if filepath.Clean(target) != filepath.Clean(shared) {
		t.Fatalf("Team alias = %q want %q", target, shared)
	}
}

func TestPrepareLiteTeamSharedWorkspacePreservesNonEmptyPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-8")
	shared := filepath.Join(root, "teams", "user-1", "team-55-shared")
	privateDir := filepath.Join(workspace, "home", ".openclaw", "workspace", "team")
	if err := os.MkdirAll(privateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "old.md"), []byte("legacy"), 0o640); err != nil {
		t.Fatal(err)
	}
	req := CreateGatewayRequest{
		AgentType: "openclaw",
		UserID:    1,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_ID":         "55",
			"CLAWMANAGER_TEAM_MEMBER_ID":  "pm",
			"CLAWMANAGER_TEAM_SHARED_DIR": shared,
		},
	}
	if err := PrepareLiteTeamSharedWorkspace(root, req, workspace, filepath.Join(workspace, "home", ".openclaw", "workspace")); err != nil {
		t.Fatal(err)
	}
	backup := privateDir + ".private-backup-pm"
	if data, err := os.ReadFile(filepath.Join(backup, "old.md")); err != nil || string(data) != "legacy" {
		t.Fatalf("private Team workspace was not preserved: data=%q err=%v", string(data), err)
	}
	if target, err := os.Readlink(privateDir); err != nil || filepath.Clean(target) != filepath.Clean(shared) {
		t.Fatalf("Team alias target=%q err=%v", target, err)
	}
}
