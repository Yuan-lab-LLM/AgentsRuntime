package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNFSSkillScanDoesNotTurnReadFailureIntoEmptyInventory(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSkillRootsChecked(root, filepath.Join(root, "skills")); err == nil {
		t.Fatal("redirected unreadable skill root reported authoritative empty inventory")
	}
}

func TestNFSSkillScanReadsPrivateSkillMetadata(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "private")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("private skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "skill.json"), []byte(`{"id":"private-id","version":"2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := ScanSkillRootsChecked(root, filepath.Join(root, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SkillID != "private-id" || records[0].SkillVersion != "2" || records[0].ContentMD5 == "" {
		t.Fatalf("private skill metadata missing: %+v", records)
	}
}

func TestNFSFailedSkillScanDoesNotPublishAuthoritativeEmptyInstance(t *testing.T) {
	cfg := Config{SandboxProfile: "nfs-rootless-v1", RuntimeType: "openclaw", WorkspaceRoot: t.TempDir()}
	manager := NewGatewayManager(cfg, nil, nil)
	manager.gateways["gw-2-1"] = &gatewayRecord{state: GatewayState{GatewayID: "gw-2-1", InstanceID: 2, UserID: 1, WorkspacePath: filepath.Join(cfg.WorkspaceRoot, "openclaw", "user-1", "instance-2")}}
	report := BuildSkillReportPayloadForInstance(cfg, manager, 1, "full", 2)
	if len(report.Instances) != 0 {
		t.Fatal("failed or unknown credential scan cleared authoritative skill inventory")
	}
}
