package openclaw

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func securityRequest(t *testing.T, workspace string) gateway.CreateGatewayRequest {
	t.Helper()
	if os.Getuid() <= 0 || os.Getgid() <= 0 {
		t.Skip("run as a positive instance UID/GID")
	}
	return gateway.CreateGatewayRequest{InstanceID: 7, UserID: 3, Generation: 1, UID: os.Getuid(), GID: os.Getgid(), WorkspacePath: workspace}
}

func TestOpenClawPreparationRejectsConfigSymlinkEscape(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
			home := filepath.Join(workspace, "home")
			victimDir := filepath.Join(root, "openclaw", "user-3", "instance-8", "private")
			if err := os.MkdirAll(victimDir, 0o755); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(victimDir, "openclaw.json")
			original := []byte(`{"victim":"unchanged"}`)
			if err := os.WriteFile(victim, original, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			persistent := filepath.Join(home, ".openclaw")
			if directory {
				if err := os.Symlink(victimDir, persistent); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(persistent, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(persistent, "openclaw.json")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(openClawDefaultsDirEnv, filepath.Join(root, "absent-defaults"))
			err := WriteGatewayConfig(gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayAuthMode: "trusted-proxy"}, securityRequest(t, workspace), workspace, 20000)
			got, readErr := os.ReadFile(victim)
			if readErr != nil || string(got) != string(original) {
				t.Fatalf("external config victim changed even if later preparation failed: err=%v data=%s read=%v", err, got, readErr)
			}
			if err == nil {
				t.Fatal("escaping config symlink accepted")
			}
		})
	}
}

func TestOpenClawPreparationRejectsRegistrySymlinkEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
	active := filepath.Join(workspace, "home", ".openclaw")
	defaults := filepath.Join(root, "trusted-defaults")
	if err := os.MkdirAll(defaults, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(active, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "sibling-registry.json")
	original := []byte(`{"installPath":"` + filepath.ToSlash(defaults) + `/npm/example"}`)
	if err := os.WriteFile(victim, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(active, "plugins", "installs.json")); err != nil {
		t.Fatal(err)
	}
	err := seedOpenClawPluginRuntimeFrom(defaults, securityRequest(t, workspace), active)
	got, readErr := os.ReadFile(victim)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("external plugin registry changed: err=%v data=%s read=%v", err, got, readErr)
	}
	if err == nil {
		t.Fatal("escaping registry symlink accepted")
	}
}

func TestOpenClawPreparationCronTempCannotOverwriteSibling(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
	cron := filepath.Join(workspace, "home", ".openclaw", "cron")
	if err := os.MkdirAll(cron, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "sibling-private.txt")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(cron, "jobs.json.tmp")); err != nil {
		t.Fatal(err)
	}
	req := securityRequest(t, workspace)
	req.Environment = map[string]string{"CLAWMANAGER_OPENCLAW_SCHEDULED_TASKS_JSON": `{"schemaVersion":1,"items":[{"id":1,"type":"scheduled_task","key":"daily","name":"daily","content":{"schemaVersion":1,"kind":"scheduled_task","format":"task/openclaw-cron@v1","config":{"name":"daily","schedule":{"kind":"cron","expr":"0 9 * * *"},"sessionTarget":"isolated","wakeMode":"now","payload":{"kind":"agentTurn","message":"fixture"}}}}]}`}
	t.Setenv(openClawDefaultsDirEnv, filepath.Join(root, "absent-defaults"))
	err := WriteGatewayConfig(gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayAuthMode: "trusted-proxy"}, req, workspace, 20000)
	got, readErr := os.ReadFile(victim)
	if readErr != nil || string(got) != "unchanged" {
		t.Fatalf("cron staging changed external victim even on later error: err=%v read=%v", err, readErr)
	}
}

func TestOpenClawPreparationPreservesReadOnlyImagePackageLinks(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
	defaults := filepath.Join(root, "image-defaults")
	connector := filepath.Join(defaults, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	if err := os.MkdirAll(connector, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(connector, "index.js")
	if err := os.WriteFile(source, []byte("immutable-image-plugin"), 0o444); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "image-global", "openclaw")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := openClawGlobalPackageDir
	openClawGlobalPackageDir = global
	defer func() { openClawGlobalPackageDir = previous }()
	if err := os.Symlink(global, filepath.Join(defaults, "npm", "node_modules", "openclaw")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(openClawDefaultsDirEnv, defaults)
	t.Setenv(openClawNPMRuntimeModeEnv, openClawNPMRuntimeModeShared)
	req := securityRequest(t, workspace)
	for range 2 {
		if err := WriteGatewayConfig(gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayAuthMode: "trusted-proxy"}, req, workspace, 20000); err != nil {
			t.Fatal(err)
		}
	}
	installed := filepath.Join(workspace, "home", ".openclaw", "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	if target, err := os.Readlink(installed); err != nil || target != connector {
		t.Fatalf("image package link lost: %q %v", target, err)
	}
	info, err := os.Stat(source)
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("image package permissions modified: %v %v", info, err)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "immutable-image-plugin" {
		t.Fatal("image package changed")
	}
}

func TestOpenClawPreparationBoundsConcurrentDirectorySwap(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
	active := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(root, "sibling-private")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(victimDir, "openclaw.json")
	original := []byte(`{"victim":"unchanged"}`)
	if err := os.WriteFile(victim, original, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(openClawDefaultsDirEnv, filepath.Join(root, "absent-defaults"))
	req := securityRequest(t, workspace)
	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		backup := active + ".private-swap"
		for {
			select {
			case <-stop:
				return
			default:
			}
			if os.Rename(active, backup) != nil {
				continue
			}
			if os.Symlink(victimDir, active) == nil {
				_ = os.Remove(active)
			}
			_ = os.Rename(backup, active)
		}
	}()
	for range 20 {
		// A race may fail preparation; it must never grant access to the victim.
		_ = WriteGatewayConfig(gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayAuthMode: "trusted-proxy"}, req, workspace, 20000)
	}
	close(stop)
	workers.Wait()
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != string(original) {
		t.Fatal("directory swap escaped the bounded instance root")
	}
	entries, err := os.ReadDir(victimDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory swap created files in sibling: %v %v", entries, err)
	}
}

func TestOpenClawPreparationRejectsPluginStagingParentEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-3", "instance-7")
	active := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "image-plugin")
	writeManagedRedisTeamPlugin(t, source, "0.3.1", "immutable-managed-plugin")
	t.Setenv(openClawRedisTeamPluginDirEnv, source)
	victim := filepath.Join(root, "sibling-extensions")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "private.txt"), []byte("unchanged"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(active, "extensions")); err != nil {
		t.Fatal(err)
	}
	if err := seedOpenClawRedisTeamPlugin(securityRequest(t, workspace), workspace); err == nil {
		t.Fatal("plugin staging accepted escaping parent")
	}
	entries, err := os.ReadDir(victim)
	if err != nil || len(entries) != 1 || entries[0].Name() != "private.txt" {
		t.Fatalf("plugin staging reached sibling: %v %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(victim, "private.txt"))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatal("plugin preparation changed victim permissions")
	}
}
