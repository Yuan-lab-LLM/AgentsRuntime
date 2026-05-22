package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

func TestSyncDefaultsCopiesWhenStateMissing(t *testing.T) {
	root := t.TempDir()
	defaultsDir := filepath.Join(root, "defaults")
	stateDir := filepath.Join(root, "state")

	if err := os.MkdirAll(filepath.Join(defaultsDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	configBytes := []byte(`{"hello":"world"}`)
	if err := os.WriteFile(filepath.Join(defaultsDir, "openclaw.json"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultsDir, "nested", "child.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := appconfig.Config{
		OpenClawDefaultsDir: defaultsDir,
		OpenClawConfigPath:  filepath.Join(stateDir, "openclaw.json"),
	}
	if err := syncDefaults(cfg); err != nil {
		t.Fatal(err)
	}

	gotConfig, err := os.ReadFile(filepath.Join(stateDir, "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotConfig) != string(configBytes) {
		t.Fatalf("expected copied config %q, got %q", configBytes, gotConfig)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "nested", "child.json")); err != nil {
		t.Fatalf("expected nested child to be copied: %v", err)
	}
}

func TestSyncDefaultsDoesNotOverwriteExistingConfig(t *testing.T) {
	root := t.TempDir()
	defaultsDir := filepath.Join(root, "defaults")
	stateDir := filepath.Join(root, "state")

	if err := os.MkdirAll(defaultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultsDir, "openclaw.json"), []byte(`{"from":"defaults"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userContent := []byte(`{"from":"user"}`)
	if err := os.WriteFile(filepath.Join(stateDir, "openclaw.json"), userContent, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := appconfig.Config{
		OpenClawDefaultsDir: defaultsDir,
		OpenClawConfigPath:  filepath.Join(stateDir, "openclaw.json"),
	}
	if err := syncDefaults(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(stateDir, "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(userContent) {
		t.Fatalf("expected user content preserved, got %q", got)
	}
}

func TestEnsureExtensionsDirCreates(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "extensions")
	cfg := appconfig.Config{OpenClawExtensionsDir: target}
	if err := ensureExtensionsDir(cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory at %s", target)
	}
}

func TestSyncBundledRedisTeamPluginUpdatesExistingCopy(t *testing.T) {
	root := t.TempDir()
	defaultsDir := filepath.Join(root, "defaults")
	extensionsDir := filepath.Join(root, "extensions")
	defaultPlugin := filepath.Join(defaultsDir, "extensions", "redis-team")
	userPlugin := filepath.Join(extensionsDir, "redis-team")

	writeRedisTeamPluginForTest(t, defaultPlugin, "0.1.1", "new runtime")
	writeRedisTeamPluginForTest(t, userPlugin, "0.1.0", "old runtime")

	cfg := appconfig.Config{
		OpenClawDefaultsDir:   defaultsDir,
		OpenClawExtensionsDir: extensionsDir,
	}
	if err := syncBundledRedisTeamPlugin(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(userPlugin, "dist", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new runtime" {
		t.Fatalf("expected redis-team plugin to be updated, got %q", got)
	}
}

func TestEnsureTeamSharedDirsDisabledNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "")
	t.Setenv("CLAWMANAGER_TEAM_SHARED_DIR", root)

	if err := ensureTeamSharedDirs(appconfig.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected disabled team bootstrap to leave %s absent, got err=%v", root, err)
	}
}

func TestEnsureTeamSharedDirsCreatesExpectedLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "true")
	t.Setenv("CLAWMANAGER_TEAM_SHARED_DIR", root)

	if err := ensureTeamSharedDirs(appconfig.Config{DropUserName: ""}); err != nil {
		t.Fatal(err)
	}

	for _, path := range append([]string{root}, teamSharedSubdirPaths(root)...) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", path)
		}
	}
}

func TestEnsureTeamSharedDirsWritesInitialStatusFromRole(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "true")
	t.Setenv("CLAWMANAGER_TEAM_SHARED_DIR", root)
	t.Setenv("CLAWMANAGER_TEAM_ID", "team-1")
	t.Setenv("CLAWMANAGER_TEAM_ROLE", "developer")
	t.Setenv("CLAWMANAGER_TEAM_MEMBER_ID", "")

	if err := ensureTeamSharedDirs(appconfig.Config{DropUserName: ""}); err != nil {
		t.Fatal(err)
	}

	statusPath := filepath.Join(root, "status", "developer.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("expected initial status file: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("invalid status JSON: %v", err)
	}
	if got := status["memberId"]; got != "developer" {
		t.Fatalf("memberId = %v, want developer", got)
	}
	if got := status["role"]; got != "developer" {
		t.Fatalf("role = %v, want developer", got)
	}
}

func TestEnsureTeamSharedDirsRejectsRelativePath(t *testing.T) {
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "true")
	t.Setenv("CLAWMANAGER_TEAM_SHARED_DIR", "relative-team")

	if err := ensureTeamSharedDirs(appconfig.Config{}); err == nil {
		t.Fatal("expected relative team shared dir to fail")
	}
}

func TestEnsureDingtalkOpenclawSymlinkCreates(t *testing.T) {
	global := filepath.Join(t.TempDir(), "openclaw")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	old := openclawGlobalNodeModules
	t.Cleanup(func() { openclawGlobalNodeModules = old })
	openclawGlobalNodeModules = global

	root := t.TempDir()
	ext := filepath.Join(root, "extensions")
	if err := os.MkdirAll(filepath.Join(ext, "dingtalk-connector"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := appconfig.Config{OpenClawExtensionsDir: ext, DropUserName: ""}
	if err := ensureDingtalkOpenclawSymlink(cfg); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ext, "dingtalk-connector", "node_modules", "openclaw")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != global {
		t.Fatalf("link target: want %q, got %q", global, target)
	}
}

func TestSyncAutostartInstallsOnlyMissing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "defaults-autostart")
	dst := filepath.Join(root, "user-autostart")

	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	newEntry := []byte("[Desktop Entry]\nName=New\n")
	existingEntry := []byte("[Desktop Entry]\nName=Existing Default\n")
	userOverride := []byte("[Desktop Entry]\nName=User Override\n")

	if err := os.WriteFile(filepath.Join(src, "new.desktop"), newEntry, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "existing.desktop"), existingEntry, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "existing.desktop"), userOverride, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := appconfig.Config{
		AutostartDefaultsDir: src,
		AutostartTargetDir:   dst,
	}
	if err := syncAutostart(cfg); err != nil {
		t.Fatal(err)
	}

	gotNew, err := os.ReadFile(filepath.Join(dst, "new.desktop"))
	if err != nil {
		t.Fatalf("expected new.desktop to be installed: %v", err)
	}
	if string(gotNew) != string(newEntry) {
		t.Fatalf("new.desktop content mismatch: %q", gotNew)
	}

	gotExisting, err := os.ReadFile(filepath.Join(dst, "existing.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotExisting) != string(userOverride) {
		t.Fatalf("expected user override preserved, got %q", gotExisting)
	}
}

func TestApplyOwnershipNoopWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test only meaningful for non-root execution")
	}
	cfg := appconfig.Config{
		OpenClawConfigPath: filepath.Join(t.TempDir(), "nonexistent", "openclaw.json"),
		DropUserName:       "abc",
	}
	if err := applyOwnership(cfg); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestDropPrivilegesNoopWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test only meaningful for non-root execution")
	}
	cfg := appconfig.Config{DropUserName: "abc"}
	if err := dropPrivileges(cfg); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func writeRedisTeamPluginForTest(t *testing.T, root, version, runtime string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "openclaw.plugin.json"), []byte(`{"id":"redis-team"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.js"), []byte(runtime), 0o644); err != nil {
		t.Fatal(err)
	}
}
