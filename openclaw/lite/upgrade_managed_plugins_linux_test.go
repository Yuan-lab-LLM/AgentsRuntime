//go:build linux

package openclaw

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeRebuildRepairsImageOwnedDanglingDependency(t *testing.T) {
	if os.Getenv("CLAWMANAGER_TEST_IMAGE_PLUGIN_FIXTURE") != "1" {
		t.Skip("isolated Runtime image fixture required")
	}
	base := "/defaults/.openclaw/npm/projects"
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	image, err := os.MkdirTemp(base, "cm-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(image) // exact MkdirTemp-owned fixture, never defaults root
	if err := os.MkdirAll(filepath.Join(image, "node_modules", "current"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(image, "package.json"), []byte(`{"dependencies":{"current":"1.0.0"}}`), 0600)
	os.WriteFile(filepath.Join(image, "package-lock.json"), []byte(`{"packages":{"":{"dependencies":{"current":"1.0.0"}}}}`), 0600)
	if err := os.WriteFile(filepath.Join(image, "node_modules", "current", "package.json"), []byte(`{"name":"current","version":"1.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	project := filepath.Join(state, "npm", "projects", filepath.Base(image))
	modules := filepath.Join(project, "node_modules")
	os.MkdirAll(modules, 0755)
	os.WriteFile(filepath.Join(project, "package.json"), []byte(`{"dependencies":{"current":"0.9.0"}}`), 0600)
	os.WriteFile(filepath.Join(project, "package-lock.json"), []byte(`{"packages":{"":{"dependencies":{"current":"0.9.0"}}}}`), 0600)
	os.Symlink(filepath.Join(image, "node_modules/current"), filepath.Join(modules, "current"))
	os.WriteFile(filepath.Join(state, "openclaw.json"), []byte(`{}`), 0600)
	os.WriteFile(filepath.Join(state, "history.sqlite"), []byte("unique-history"), 0600)
	os.WriteFile(filepath.Join(project, "custom-note.txt"), []byte("user-owned-override"), 0600)
	os.MkdirAll(filepath.Join(modules, "custom-local"), 0755)
	os.WriteFile(filepath.Join(modules, "custom-local", "data.txt"), []byte("custom-plugin-data"), 0600)
	os.Symlink(filepath.Join(image, "node_modules", "bluebird"), filepath.Join(modules, "bluebird"))
	os.Symlink(filepath.Join(state, "history.sqlite"), filepath.Join(state, "history-link"))
	req := WorkspaceUpgradeRequest{RolloutID: "broken-plugin", InstanceID: 7, UserID: 1, Generation: 8, UID: 200007, GID: 200007}
	// Original capture must preserve, not dereference/drop, the dangling link.
	dest := filepath.Join(t.TempDir(), "npm")
	if _, _, err := copyUpgradePluginTree(filepath.Join(state, "npm"), dest, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(dest, "projects", filepath.Base(image), "node_modules", "bluebird")); err != nil {
		t.Fatal(err)
	}
	if err := validateUpgradePluginLinks(state); err == nil {
		t.Fatal("ordinary preflight swallowed the broken link")
	}
	proof, err := m.rebuildPreservingState(context.Background(), workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if proof.CandidateSHA256 == "" || proof.OriginalSHA256 == proof.CandidateSHA256 {
		t.Fatal("fresh environment was only a clone")
	}
	for _, rel := range []string{"history.sqlite", "npm/projects/" + filepath.Base(image) + "/custom-note.txt", "npm/projects/" + filepath.Base(image) + "/node_modules/custom-local/data.txt"} {
		old, e := os.ReadFile(filepath.Join(proof.OriginalPath, rel))
		if e != nil {
			t.Fatal(e)
		}
		current, e := os.ReadFile(filepath.Join(state, rel))
		if e != nil || string(old) != string(current) {
			t.Fatal("durable data changed", e)
		}
	}
	if _, err := os.Readlink(filepath.Join(proof.OriginalPath, "npm/projects", filepath.Base(image), "node_modules/bluebird")); err != nil {
		t.Fatal("original link lost", err)
	}
	if err := validateUpgradePluginLinks(state); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(state, "history-link")); err != nil || string(raw) != "unique-history" {
		t.Fatal("internal link did not survive cutover", err)
	}
	if _, err := m.rebuildPreservingState(context.Background(), workspace, req); err != nil {
		t.Fatal("receipt resume failed", err)
	}
}

func TestUpgradeProbeCannotSwallowPluginMissingInput(t *testing.T) {
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(filepath.Join(state, "extensions"), 0700)
	os.Symlink(filepath.Join(state, "missing-custom-plugin"), filepath.Join(state, "extensions", "custom"))
	req := WorkspaceUpgradeRequest{InstanceID: 7, UID: 200007, GID: 200007}
	if _, _, _, err := m.createUpgradeCompatibilityProbe(workspace, map[string]any{}, req); err == nil {
		t.Fatal("missing plugin input was silently ignored")
	}
}
