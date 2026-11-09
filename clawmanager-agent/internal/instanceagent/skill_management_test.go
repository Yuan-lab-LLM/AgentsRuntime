package instanceagent

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAndUninstallSkillArchive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	archive := testSkillZIP(t, map[string]string{
		"weather-tool/SKILL.md":     "# Weather\n",
		"weather-tool/scripts/a.sh": "echo ok\n",
	})
	result, err := InstallSkillArchive(root, map[string]any{
		"skill_id":      "skill-12",
		"skill_version": "skill-version-34",
		"target_name":   "weather-tool",
	}, archive)
	if err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(root, "weather-tool")
	if result["install_path"] != installPath {
		t.Fatalf("result = %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(installPath, "SKILL.md")); err != nil || string(data) != "# Weather\n" {
		t.Fatalf("installed SKILL.md = %q, %v", data, err)
	}

	removed, err := UninstallSkill(root, map[string]any{"target_name": "weather-tool"})
	if err != nil {
		t.Fatal(err)
	}
	if removed["status"] != "removed" {
		t.Fatalf("removed = %+v", removed)
	}
	if _, err := os.Stat(installPath); !os.IsNotExist(err) {
		t.Fatalf("skill still exists: %v", err)
	}
}

func TestInstallSkillArchiveRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	archive := testSkillZIP(t, map[string]string{
		"../escape/SKILL.md": "bad",
	})
	if _, err := InstallSkillArchive(root, map[string]any{"target_name": "safe"}, archive); err == nil {
		t.Fatal("zip traversal was accepted")
	}
}

func TestInstallSkillArchiveRejectsTargetEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	archive := testSkillZIP(t, map[string]string{"SKILL.md": "# Skill\n"})
	if _, err := InstallSkillArchive(root, map[string]any{"target_name": "../escape"}, archive); err == nil {
		t.Fatal("target escape was accepted")
	}
}

func testSkillZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
