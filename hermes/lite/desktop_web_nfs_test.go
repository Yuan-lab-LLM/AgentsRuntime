package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopWebNFSPreservesGrantedWorkspaceAndPrivateHome(t *testing.T) {
	root := t.TempDir()
	req := strictTestRequest(root)
	req.UID, req.GID = os.Getuid(), os.Getgid()
	if err := os.MkdirAll(filepath.Join(req.WorkspacePath, "home", ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(req.WorkspacePath, 0770); err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "hermes", SandboxProfile: "nfs-rootless-v1"}
	home, err := openDesktopWebHome(cfg, req, req.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	home.Close()
	for path, mode := range map[string]os.FileMode{req.WorkspacePath: 0770, filepath.Join(req.WorkspacePath, "home"): 0700, filepath.Join(req.WorkspacePath, "home", ".hermes"): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("NFS existing grant/private directory changed: %s %v %v", path, info, err)
		}
	}
}
