//go:build linux

package gateway

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNFSTeamConfigHelper(t *testing.T) {
	root := os.Getenv("CM_NFS_TEAM_ROOT")
	if root == "" {
		return
	}
	team := os.Getenv("CM_NFS_TEAM_ID")
	if team == "" {
		team = "7"
	}
	group := os.Getenv("CM_NFS_TEAM_GID")
	if group == "" {
		group = "300001"
	}
	shared := filepath.Join(root, "teams", "user-1", "team-"+team+"-shared")
	req := CreateGatewayRequest{UserID: 1, InstanceID: os.Geteuid() - 200000, UID: os.Geteuid(), GID: os.Getegid(), Environment: map[string]string{"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_ID": team, "CLAWMANAGER_TEAM_SHARED_GID": group, "CLAWMANAGER_TEAM_SHARED_DIR": shared, "CLAWMANAGER_TEAM_CONFIG_JSON": `{"ok":true}`}}
	if err := WriteLiteTeamConfigJSON(req, filepath.Join(root, "openclaw", "user-1", fmt.Sprintf("instance-%d", req.InstanceID))); err != nil {
		t.Fatal(err)
	}
}

func TestNFSTeamConfigDoesNotChgrpManagementOwnedSharedRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires disposable credential fixture")
	}
	root, err := os.MkdirTemp("/srv", "cm-storage-team-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	os.Chmod(root, 0755)
	shared := filepath.Join(root, "teams", "user-1", "team-7-shared")
	if err = os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(shared, 10001, 300001); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(shared, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	helper := filepath.Join(root, "helper")
	out, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(out, in)
	out.Close()
	cmd := exec.Command(helper, "-test.run=^TestNFSTeamConfigHelper$")
	cmd.Env = []string{"CM_NFS_TEAM_ROOT=" + root}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200002, Gid: 200002, Groups: []uint32{300001}}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("instance Team config failed: %v %s", err, output)
	}
}

func TestNFSTeamAliasPreservesExistingOwnership(t *testing.T) {
	if os.Getenv("CM_NFS_TEAM_ALIAS_ACCEPTANCE") != "1" || os.Geteuid() != 200002 {
		t.Skip("requires instance200002 and supplemental Team300001")
	}
	root := t.TempDir()
	for _, parent := range []string{".openclaw/workspace", ".hermes"} {
		workspace := filepath.Join(root, strings.ReplaceAll(parent, "/", "-"))
		aliasParent := filepath.Join(workspace, parent)
		shared := filepath.Join(workspace, "team")
		if err := os.MkdirAll(aliasParent, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(aliasParent, 200002, 300001); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(aliasParent, os.ModeSetgid|0750); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(shared, 0770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(shared, 200002, 300001); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(aliasParent, "private")
		if err := os.WriteFile(sentinel, []byte("retained-private"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(shared, filepath.Join(aliasParent, "team")); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(aliasParent)
		req := CreateGatewayRequest{UserID: 1, InstanceID: 2, UID: 200002, GID: 200002, Environment: map[string]string{"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_SHARED_GID": "300001", "CLAWMANAGER_TEAM_SHARED_DIR": shared}}
		for i := 0; i < 2; i++ {
			if err := PrepareLiteTeamSharedWorkspaceForConfig(Config{SandboxProfile: "nfs-rootless-v1", WorkspaceRoot: root}, req, workspace, parent); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(aliasParent)
			a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
			if a.Uid != b.Uid || a.Gid != b.Gid || before.Mode() != after.Mode() {
				t.Fatalf("alias parent metadata changed: %v -> %v", a, b)
			}
			data, err := os.ReadFile(sentinel)
			info, _ := os.Stat(sentinel)
			if err != nil || string(data) != "retained-private" || info.Mode().Perm() != 0600 {
				t.Fatalf("private file changed: %v", err)
			}
		}
	}
}
