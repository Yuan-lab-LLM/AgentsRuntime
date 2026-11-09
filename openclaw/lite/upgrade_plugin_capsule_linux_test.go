//go:build linux

package openclaw

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpgrade102CapsuleRestoresPluginFilesAndLinks(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: "2026.10.2", WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "plugins", InstanceID: 31, UserID: 1, Generation: 1, UID: os.Getuid(), GID: os.Getgid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(workspace, "home", ".openclaw")
	payload := filepath.Join(state, "extensions", "sample", "package.json")
	if err := os.MkdirAll(filepath.Dir(payload), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("old-plugin"), 0600); err != nil {
		t.Fatal(err)
	}
	nodeModules := filepath.Join(state, "npm", "node_modules")
	if err := os.MkdirAll(nodeModules, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(nodeModules, "sample")
	if err := os.Symlink(filepath.Dir(payload), link); err != nil {
		t.Fatal(err)
	}
	capsule, err := mgr.prepareOpenClawStateCapsule(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.SchemaVersion != 3 || !capsule.ControlDirs["npm"] || !capsule.ControlDirs["extensions"] {
		t.Fatalf("incomplete capsule %+v", capsule)
	}
	if err := os.WriteFile(payload, []byte("new-plugin"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(state, "plugin-skills", "new-only.txt")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("new-only"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // Retry must preserve the already restored source.
		if ok, err := mgr.restoreOpenClawStateCapsule(workspace, req); err != nil || !ok {
			t.Fatalf("restore %v %v", ok, err)
		}
	}
	data, err := os.ReadFile(payload)
	if err != nil || string(data) != "old-plugin" {
		t.Fatalf("payload=%s err=%v", data, err)
	}
	value, err := os.Readlink(link)
	if err != nil || value != filepath.Dir(payload) {
		t.Fatalf("link=%s err=%v", value, err)
	}
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatalf("target-only skill remained: %v", err)
	}
}

func TestUpgradePluginCapsuleRejectsExternalLinks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "state", "npm")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyUpgradePluginTree(source, filepath.Join(root, "capsule"), filepath.Join(root, "state")); err == nil {
		t.Fatal("external file link accepted")
	}
}
