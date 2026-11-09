package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedPluginPlanMigratesMetadataAndObsoleteDependencies(t *testing.T) {
	root := t.TempDir()
	image, state := filepath.Join(root, "image"), filepath.Join(root, "state")
	old := filepath.Join(state, "npm/projects/old-id")
	fresh := filepath.Join(image, "npm/projects/new-id")
	write := func(name, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(old, "package.json"), `{"dependencies":{"@sample/channel":"1.0.0"}}`)
	write(filepath.Join(old, "package-lock.json"), `{"packages":{"":{"dependencies":{"@sample/channel":"1.0.0"}}}}`)
	write(filepath.Join(fresh, "package.json"), `{"private":true,"dependencies":{"@sample/channel":"2.0.0"},"overrides":{"axios":"1.20.0"},"openclaw":{"managedOverrides":["axios"]}}`)
	write(filepath.Join(fresh, "package-lock.json"), `{"packages":{"":{"dependencies":{"@sample/channel":"2.0.0"}}}}`)
	write(filepath.Join(fresh, "node_modules/@sample/channel/package.json"), `{"name":"@sample/channel","version":"2.0.0"}`)
	write(filepath.Join(old, "custom.txt"), "user-data")
	if err := os.MkdirAll(filepath.Join(old, "node_modules/@sample"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(image, "npm/projects/old-id/node_modules/@sample/channel"), filepath.Join(old, "node_modules/@sample/channel")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(image, "npm/projects/old-id/node_modules/bluebird"), filepath.Join(old, "node_modules/bluebird")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "npm/node_modules/@sample"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(image, "npm/projects/old-id/node_modules/@sample/channel"), filepath.Join(state, "npm/node_modules/@sample/channel")); err != nil {
		t.Fatal(err)
	}
	plan, err := planManagedPlugins(state, image)
	if err != nil || len(plan.Changes) != 1 || plan.SHA256 == "" {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	if err = applyManagedPluginPlan(state, image, plan); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		b, _ := os.ReadFile(filepath.Join(old, name))
		if !strings.Contains(string(b), "2.0.0") {
			t.Fatalf("stale %s: %s", name, b)
		}
	}
	if _, err = os.Lstat(filepath.Join(old, "node_modules/bluebird")); !os.IsNotExist(err) {
		t.Fatal("obsolete link retained", err)
	}
	b, _ := os.ReadFile(filepath.Join(old, "custom.txt"))
	if string(b) != "user-data" {
		t.Fatal("custom file replaced")
	}
	if _, err = os.Stat(filepath.Join(old, "node_modules/@sample/channel/package.json")); err != nil {
		t.Fatal("new image payload not linked", err)
	}
	if _, err = os.Stat(filepath.Join(state, "npm/node_modules/@sample/channel/package.json")); err != nil {
		t.Fatal("legacy alias was not migrated", err)
	}
	second, err := planManagedPlugins(state, image)
	if err != nil || len(second.Changes) != 0 {
		t.Fatalf("not idempotent %+v %v", second, err)
	}
}

func TestManagedPluginMetadataRejectsUserScriptsAndOverrides(t *testing.T) {
	for _, raw := range []string{`{"dependencies":{"x":"1"},"scripts":{"postinstall":"echo custom"}}`, `{"dependencies":{"x":"1"},"overrides":{"axios":"custom"}}`} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "package.json"), []byte(raw), 0600)
		if _, _, _, err := pluginProjectMetadata(root); err == nil || !strings.Contains(err.Error(), "CUSTOM_REVIEW_REQUIRED") {
			t.Fatal("custom metadata accepted", err)
		}
	}
}
