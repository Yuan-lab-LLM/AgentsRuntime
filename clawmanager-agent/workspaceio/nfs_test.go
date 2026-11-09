//go:build linux

package workspaceio

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNFSManagementRootRejectsUntrustedOwnerWithoutRepair(t *testing.T) {
	root := t.TempDir()
	if err := os.Chown(root, 200001, 200001); err != nil {
		t.Fatal(err)
	}
	if err := PrepareNFSManagementRoots(root, "openclaw"); err == nil {
		t.Fatal("business-owned management root accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "openclaw")); !os.IsNotExist(err) {
		t.Fatal("untrusted root mutated")
	}
}

func TestNFSInstanceGrantRejectsSymlinkAndPreservesExistingLeaf(t *testing.T) {
	root := t.TempDir()
	if err := os.Chown(root, 10001, 10001); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "openclaw")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareNFSInstance(root, "openclaw", 1, 2, 200002, 200002); err == nil {
		t.Fatal("redirected ancestor accepted")
	}
}

func TestNFSStorageProcessHelper(t *testing.T) {
	if os.Getenv("CM_NFS_STORAGE_CHILD") == "" {
		return
	}
	root := os.Getenv("CM_NFS_STORAGE_ROOT")
	identity := os.Getegid()
	instance := identity - 200000
	if err := PrepareNFSInstance(root, "openclaw", 1, instance, identity, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNFSTmp(root, "openclaw", 1, instance, identity, identity); err != nil {
		t.Fatal(err)
	}
}

func TestNFSInstanceCredentialsCannotReadSiblingPrivateData(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires disposable credential fixture")
	}
	localRoot, err := os.MkdirTemp("/srv", "cm-storage-grant-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(localRoot)
	root := localRoot
	if fixture := os.Getenv("CM_NFS_STORAGE_FIXTURE"); fixture != "" {
		root = fixture
	}
	if err = os.Chmod(localRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if root == localRoot {
		if err = os.Chown(root, 10001, 10001); err != nil {
			t.Fatal(err)
		}
	}
	if root == localRoot {
		if err = os.Chmod(root, 0711); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	helper := filepath.Join(localRoot, "test-helper")
	target, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	target.Close()
	grant := func(gid uint32) {
		cmd := exec.Command(helper, "-test.run=^TestNFSStorageProcessHelper$")
		cmd.Env = []string{"CM_NFS_STORAGE_CHILD=1", "CM_NFS_STORAGE_ROOT=" + root}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10001, Gid: gid}}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("management grant: %v %s", err, output)
		}
	}
	grant(200002)
	grant(200003)
	leaf := filepath.Join(root, "openclaw", "user-1", "instance-2")
	command := func(uid uint32, script string) error {
		c := exec.Command("/bin/sh", "-c", script, "storage-test", leaf, filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2", "tmp"), filepath.Join(root, "openclaw", "user-1", "instance-3"))
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		if output, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("%w %s", err, output)
		}
		return nil
	}
	if err = command(200002, `umask 077; printf private > "$1/secret"; printf durable > "$2/saved"`); err != nil {
		t.Fatal(err)
	}
	if err = command(200003, `cat "$1/secret"`); err == nil {
		t.Fatal("sibling instance read private data")
	}
	if err = command(200003, `cat "$2/saved"`); err == nil {
		t.Fatal("sibling instance read persistent tmp")
	}
	if err = command(200003, `umask 077; printf sibling > "$3/secret"`); err != nil {
		t.Fatal(err)
	}
	grant(200002)
	if err = command(200002, `test "$(cat "$2/saved")" = durable`); err != nil {
		t.Fatal("A-to-B-to-A tmp retention:", err)
	}
	if err = command(200002, `before=$(stat -c '%u:%g:%a:%Y' "$1/secret"); test "$(cat "$1/secret")" = private; test "$before" = "$(stat -c '%u:%g:%a:%Y' "$1/secret")"; test "$(stat -c '%u:%g:%a' "$1/secret")" = 200002:200002:600`); err != nil {
		t.Fatal(err)
	}
	if root != localRoot {
		if _, err = os.ReadFile(filepath.Join(leaf, "secret")); err == nil {
			t.Fatal("root-squashed client unexpectedly read private data")
		}
		t.Log("root_squash denied UID0 while instance helper retained private access")
	}
}
