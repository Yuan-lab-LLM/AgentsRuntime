//go:build linux

package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

type crossMountUpgradeHost struct {
	gateway.UpgradeHost
	root string
}

func (h crossMountUpgradeHost) UpgradeRoot() string { return h.root }

func TestScopedRollbackAcrossFilesystemMountsPreservesOriginalAndRetries(t *testing.T) {
	root := t.TempDir()
	capsule, err := os.MkdirTemp("/dev/shm", "cm-native-rollback-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(capsule) })
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: "2026.10.2", WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	mgr.host = crossMountUpgradeHost{mgr.host, capsule}
	mgr.scopedNative = true
	mgr.io = newFixtureUpgradeIO(root, capsule)
	req := WorkspaceUpgradeRequest{RolloutID: "crossmount", InstanceID: 31, UserID: 1, Generation: 1, UID: os.Getuid(), GID: os.Getgid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(workspace, "home/.openclaw")
	payload := filepath.Join(state, "extensions/sample/package.json")
	if err = os.MkdirAll(filepath.Dir(payload), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(payload, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(state, "extensions/sample/relative-link")
	if err = os.Symlink("package.json", link); err != nil {
		t.Fatal(err)
	}
	if _, err = mgr.prepareOpenClawStateCapsule(workspace, req); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(payload, []byte("failed-target"), 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(state, "openclaw.json.bak.1")
	if err = os.WriteFile(backup, []byte("preserve-failed-backup"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := mgr.restoreOpenClawStateCapsule(workspace, req); err != nil || !ok {
			t.Fatalf("cross-mount restore attempt%d: %v %v", i, ok, err)
		}
	}
	data, err := os.ReadFile(payload)
	if err != nil || string(data) != "original" {
		t.Fatalf("original not restored: %s %v", data, err)
	}
	info, err := os.Stat(payload)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private mode changed")
	}
	if got, err := os.Readlink(link); err != nil || got != "package.json" {
		t.Fatal("original link changed", got, err)
	}
	if _, err = os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("failed backup remained live", err)
	}
	failed := filepath.Join(workspace, ".clawmanager-rollback", "rollout-"+req.RolloutID, "control-failed-target-files", "openclaw.json.bak.1")
	if data, err = os.ReadFile(failed); err != nil || string(data) != "preserve-failed-backup" {
		t.Fatal("failed target was not retained", err)
	}
	original := filepath.Join(mgr.configCapsuleDir(req), "control-original/extensions/sample/package.json")
	if data, err = os.ReadFile(original); err != nil || string(data) != "original" {
		t.Fatal("original capsule consumed before retry", err)
	}
}
