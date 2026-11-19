//go:build linux

package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNFSPersistedInodeChild(t *testing.T) {
	root := os.Getenv("CM_INODE_ROOT")
	if root == "" {
		return
	}
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-12")
	req := CreateGatewayRequest{UserID: 3, InstanceID: 12, Generation: 1, UID: 200012, GID: 200012, AgentType: "openclaw", SecurityProfile: "isolated-network-v1", WorkspacePath: workspace}
	prepared := false
	profile := migrationTestProfile{isolatedTestProfile: isolatedTestProfile{prepare: func(CreateGatewayRequest) error { prepared = true; return nil }}, migrate: func() error { return nil }}
	payload, _ := json.Marshal(NativeStorageRequest{Config: NativeStorageConfig{RuntimeType: "openclaw", WorkspaceRoot: root, SandboxProfile: "nfs-rootless-v1", NetworkSecurityProfile: "isolated-network-v1"}, Request: req, Port: 28080})
	err := workspaceio.ValidateNFSPrivateTree(workspace, req.UID, false)
	if err == nil {
		_, err = RunNativeStorageHelper(profile, []string{"managed-native-storage"}, bytes.NewReader(payload), io.Discard)
	}

	if os.Getenv("CM_INODE_VALID") == "1" {
		if err != nil || !prepared {
			t.Fatalf("valid own hardlinks denied: %v", err)
		}
		return
	}
	if err == nil || prepared {
		t.Fatal("foreign persisted inode reached native preparation and raw business bind admission")
	}
	fsroot, err := workspaceio.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer fsroot.Close()
	file, err := openNativeSkillInput(fsroot, "foreign.txt", false)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("foreign persisted inode accepted by skill reader")
	}
}

func TestNFSPersistedForeignInodeRejectedBeforeNative(t *testing.T) {
	root := os.Getenv("CM_INODE_FIXTURE")
	if root == "" || os.Geteuid() != 0 {
		t.Skip("requires dedicated root-squash fixture")
	}
	exe, _ := os.Executable()
	for _, kind := range []string{"hardlink", "singlelink", "own-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", `cp "$1" /srv/cm-nfs-inode-helper; chmod 755 /srv/cm-nfs-inode-helper`, "inode-test", exe)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("helper copy: %v %s", err, out)
			}
			fixture := filepath.Join(root, kind)
			cmd = exec.Command("/srv/cm-nfs-inode-helper", "-test.run=^TestNFSPersistedInodeChild$", "-test.v")
			cmd.Env = []string{"CM_INODE_ROOT=" + fixture}
			if kind == "own-hardlink" {
				cmd.Env = append(cmd.Env, "CM_INODE_VALID=1")
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200012, Gid: 200012, Groups: []uint32{}}}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("instance helper: %v %s", err, out)
			}
			t.Log(string(out))
		})
	}
}

func TestNFSPersistedTmpChild(t *testing.T) {
	root := os.Getenv("CM_INODE_TMP")
	if root == "" {
		return
	}
	err := workspaceio.ValidateNFSPrivateTree(root, 200012, true)
	if os.Getenv("CM_INODE_VALID") == "1" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil {
		t.Fatal("foreign inode admitted through persistent tmp")
	}
}

func TestNFSPersistedTmpAuditPreservesStickyGrants(t *testing.T) {
	root := os.Getenv("CM_INODE_FIXTURE")
	if root == "" || os.Geteuid() != 0 {
		t.Skip("requires dedicated root-squash fixture")
	}
	for _, kind := range []string{"sticky1770", "legacy1777", "foreign-tmp"} {
		cmd := exec.Command("/srv/cm-nfs-inode-helper", "-test.run=^TestNFSPersistedTmpChild$", "-test.v")
		cmd.Env = []string{"CM_INODE_TMP=" + filepath.Join(root, kind)}
		if kind != "foreign-tmp" {
			cmd.Env = append(cmd.Env, "CM_INODE_VALID=1")
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200012, Gid: 200012, Groups: []uint32{}}}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", kind, err, out)
		}
		t.Log(string(out))
	}
}
