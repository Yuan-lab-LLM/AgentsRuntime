package openclaw

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeNFSPluginLayoutKeepsExistingPackagesAndLinksProjectPlugin(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "home", ".openclaw", "npm", "projects", "p1", "node_modules", "plugin-a")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "openclaw.plugin.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareNativePluginLayout(workspace); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "home", ".openclaw", "npm", "node_modules", "plugin-a")
	target, err := filepath.EvalSymlinks(link)
	if err != nil || target != project {
		t.Fatalf("project plugin not bridged: %s %v", target, err)
	}
	if err := prepareNativePluginLayout(workspace); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(project, "openclaw.plugin.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("existing plugin mode changed")
	}
}
