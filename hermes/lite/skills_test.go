package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

func TestScanWorkspaceSkillsIncludesHermesSkillRoot(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "home", ".hermes", "skills", "weather")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "skill.json"), []byte(`{"id":"hermes-weather","version":"1.2.0","name":"weather"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("weather skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := gateway.ScanSkillRoots(workspace, NewProfile("hermes").SkillRoots(workspace)...)
	if len(skills) != 1 {
		t.Fatalf("skills len = %d, want 1: %#v", len(skills), skills)
	}
	if skills[0].SkillID != "hermes-weather" {
		t.Fatalf("SkillID = %q, want hermes-weather", skills[0].SkillID)
	}
	if skills[0].SkillVersion != "1.2.0" {
		t.Fatalf("SkillVersion = %q, want 1.2.0", skills[0].SkillVersion)
	}
	if skills[0].Identifier != "hermes-weather" {
		t.Fatalf("Identifier = %q, want hermes-weather", skills[0].Identifier)
	}
}

func TestScanWorkspaceSkillsIncludesNestedCategorySkill(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "home", ".hermes", "skills", "productivity", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := gateway.ScanSkillRoots(workspace, NewProfile("hermes").SkillRoots(workspace)...)
	if len(skills) != 1 {
		t.Fatalf("skills len = %d, want 1: %#v", len(skills), skills)
	}
	if skills[0].Identifier != "productivity/my-skill" {
		t.Fatalf("Identifier = %q, want productivity/my-skill", skills[0].Identifier)
	}
}
