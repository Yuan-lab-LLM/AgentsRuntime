//go:build linux

package gateway

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNFSWorkspacePreservesGrantedLeafAndPrivateData(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires disposable credential fixture")
	}
	root, err := os.MkdirTemp("/srv", "cm-storage-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	os.Chmod(root, 0755)
	req := CreateGatewayRequest{UserID: 1, InstanceID: 2, UID: 200002, GID: 200002, WorkspacePath: filepath.Join(root, "openclaw", "user-1", "instance-2")}
	if err := os.MkdirAll(req.WorkspacePath, 0770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(req.WorkspacePath, 10001, req.GID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(req.WorkspacePath, 0770); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(req.WorkspacePath)
	cfg := Config{SandboxProfile: "nfs-rootless-v1", WorkspaceRoot: root, RuntimeType: "openclaw"}
	if _, err := PrepareWorkspaceForConfig(cfg, req); err == nil {
		t.Fatal("management caller initialized private native data")
	}
	for _, parent := range []string{filepath.Join(root, "openclaw"), filepath.Join(root, "openclaw", "user-1")} {
		if err := os.Chmod(parent, 0711); err != nil {
			t.Fatal(err)
		}
	}
	exe, _ := os.Executable()
	source, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	helper := filepath.Join(root, "helper")
	target, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(target, source)
	target.Close()
	cmd := exec.Command(helper, "-test.run=^TestNFSWorkspaceHelper$")
	cmd.Env = []string{"CM_NFS_WORKSPACE_ROOT=" + root}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200002, Gid: 200002}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("instance workspace preparation: %v %s", err, output)
	}
	home, err := os.Stat(filepath.Join(req.WorkspacePath, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if home.Sys().(*syscall.Stat_t).Uid != 200002 {
		t.Fatal("native home was not created by the instance")
	}

	after, _ := os.Stat(req.WorkspacePath)
	if before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || after.Mode().Perm() != 0770 {
		t.Fatal("grant ownership or permissions changed")
	}
}

func TestNFSWorkspaceHelper(t *testing.T) {
	root := os.Getenv("CM_NFS_WORKSPACE_ROOT")
	if root == "" {
		return
	}
	req := CreateGatewayRequest{UserID: 1, InstanceID: 2, UID: 200002, GID: 200002, WorkspacePath: filepath.Join(root, "openclaw", "user-1", "instance-2")}
	cfg := Config{SandboxProfile: "nfs-rootless-v1", WorkspaceRoot: root, RuntimeType: "openclaw"}
	if _, err := PrepareWorkspaceForConfig(cfg, req); err != nil {
		t.Fatal(err)
	}
}
