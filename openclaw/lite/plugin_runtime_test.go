package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteOpenClawGatewayConfigSeedsInstancePluginRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows test runners commonly cannot create symlinks without an
		// elevated token. The copy mode remains the supported rollback path.
		t.Setenv(openClawNPMRuntimeModeEnv, openClawNPMRuntimeModeCopy)
	}
	root := t.TempDir()
	defaultsRoot := filepath.Join(root, "defaults", ".openclaw")
	feishuSource := filepath.Join(defaultsRoot, "npm", "node_modules", "@openclaw", "feishu")
	if err := os.MkdirAll(feishuSource, 0o755); err != nil {
		t.Fatalf("mkdir default npm plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(feishuSource, "index.js"), []byte("image-plugin"), 0o644); err != nil {
		t.Fatalf("write default npm plugin: %v", err)
	}
	extensionSource := filepath.Join(defaultsRoot, "extensions", "sample-channel")
	if err := os.MkdirAll(extensionSource, 0o755); err != nil {
		t.Fatalf("mkdir default extension: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extensionSource, "openclaw.plugin.json"), []byte(`{"id":"sample-channel"}`), 0o644); err != nil {
		t.Fatalf("write default extension: %v", err)
	}
	pluginsSource := filepath.Join(defaultsRoot, "plugins")
	if err := os.MkdirAll(pluginsSource, 0o755); err != nil {
		t.Fatalf("mkdir default plugin registry: %v", err)
	}
	registry := map[string]any{
		"installRecords": map[string]any{
			"feishu": map[string]any{
				"installPath": filepath.ToSlash(feishuSource),
			},
		},
		"plugins": []any{
			map[string]any{
				"pluginId":     "feishu",
				"manifestPath": filepath.ToSlash(filepath.Join(feishuSource, "openclaw.plugin.json")),
			},
		},
	}
	registryData, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal default plugin registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsSource, "installs.json"), registryData, 0o644); err != nil {
		t.Fatalf("write default plugin registry: %v", err)
	}

	linkTarget := "/usr/local/lib/node_modules/openclaw"
	if runtime.GOOS != "windows" {
		if err := os.Symlink(linkTarget, filepath.Join(defaultsRoot, "npm", "node_modules", "openclaw")); err != nil {
			t.Fatalf("create default npm peer symlink: %v", err)
		}
	}

	t.Setenv(openClawDefaultsDirEnv, defaultsRoot)
	workspace := filepath.Join(root, "workspaces", "openclaw", "user-1", "instance-256")
	home := filepath.Join(workspace, "home")
	activeRoot := filepath.Join(home, ".openclaw")
	req := CreateGatewayRequest{
		AgentType: "openclaw", UserID: 1, InstanceID: 256,
		Environment: map[string]string{
			"CLAWMANAGER_WORKSPACE_PATH":       workspace,
			"HOME":                             home,
			"CLAWMANAGER_AGENT_PERSISTENT_DIR": activeRoot,
		},
	}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20056); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	feishuPackageTarget := filepath.Join(activeRoot, "npm", "node_modules", "@openclaw", "feishu")
	feishuTarget := filepath.Join(feishuPackageTarget, "index.js")
	if got, err := os.ReadFile(feishuTarget); err != nil {
		t.Fatalf("read seeded npm plugin: %v", err)
	} else if string(got) != "image-plugin" {
		t.Fatalf("seeded npm plugin = %q, want image content", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(feishuPackageTarget)
		if err != nil {
			t.Fatalf("stat shared npm plugin: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("shared npm plugin mode = %v, want symlink", info.Mode())
		}
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "extensions", "sample-channel", "openclaw.plugin.json")); err != nil {
		t.Fatalf("stat copied extension: %v", err)
	}
	if runtime.GOOS != "windows" {
		if got, err := os.Readlink(filepath.Join(activeRoot, "npm", "node_modules", "openclaw")); err != nil {
			t.Fatalf("read copied npm peer symlink: %v", err)
		} else if got != linkTarget {
			t.Fatalf("copied npm peer symlink = %q, want %q", got, linkTarget)
		}
	}

	copiedRegistry, err := os.ReadFile(filepath.Join(activeRoot, "plugins", "installs.json"))
	if err != nil {
		t.Fatalf("read copied plugin registry: %v", err)
	}
	if strings.Contains(string(copiedRegistry), filepath.ToSlash(defaultsRoot)+"/") {
		t.Fatal("copied plugin registry still references defaults root")
	}
	if !strings.Contains(string(copiedRegistry), filepath.ToSlash(activeRoot)+"/") {
		t.Fatal("copied plugin registry does not reference instance plugin root")
	}

	configData, err := os.ReadFile(filepath.Join(activeRoot, "openclaw.json"))
	if err != nil {
		t.Fatalf("read instance config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("parse instance config: %v", err)
	}
	if got, want := objectAt(t, objectAt(t, config, "agents"), "defaults")["workspace"], filepath.ToSlash(filepath.Join(activeRoot, "workspace")); got != want {
		t.Fatalf("agents.defaults.workspace = %#v, want %q", got, want)
	}

	if runtime.GOOS != "windows" {
		if err := os.Remove(feishuPackageTarget); err != nil {
			t.Fatalf("remove shared npm plugin link: %v", err)
		}
		if err := os.MkdirAll(feishuPackageTarget, 0o755); err != nil {
			t.Fatalf("create instance npm plugin override: %v", err)
		}
	}
	if err := os.WriteFile(feishuTarget, []byte("user-modified"), 0o644); err != nil {
		t.Fatalf("modify instance plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(feishuSource, "index.js"), []byte("new-image-plugin"), 0o644); err != nil {
		t.Fatalf("modify default plugin fixture: %v", err)
	}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20056); err != nil {
		t.Fatalf("second WriteGatewayConfig() error = %v", err)
	}
	if got, err := os.ReadFile(feishuTarget); err != nil {
		t.Fatalf("read user-modified instance plugin: %v", err)
	} else if string(got) != "user-modified" {
		t.Fatalf("instance plugin was overwritten: %q", got)
	}
}

func TestSeedOpenClawPluginRuntimeCopyModeKeepsIndependentNPMFiles(t *testing.T) {
	t.Setenv(openClawNPMRuntimeModeEnv, openClawNPMRuntimeModeCopy)
	root := t.TempDir()
	defaultsRoot := filepath.Join(root, "defaults", ".openclaw")
	sourceFile := filepath.Join(defaultsRoot, "npm", "node_modules", "sample-plugin", "index.js")
	if err := os.MkdirAll(filepath.Dir(sourceFile), 0o755); err != nil {
		t.Fatalf("mkdir default npm plugin: %v", err)
	}
	if err := os.WriteFile(sourceFile, []byte("image-plugin"), 0o644); err != nil {
		t.Fatalf("write default npm plugin: %v", err)
	}

	activeRoot := filepath.Join(root, "active", ".openclaw")
	if err := seedOpenClawPluginRuntimeFrom(defaultsRoot, CreateGatewayRequest{}, activeRoot); err != nil {
		t.Fatalf("seedOpenClawPluginRuntimeFrom() error = %v", err)
	}
	targetFile := filepath.Join(activeRoot, "npm", "node_modules", "sample-plugin", "index.js")
	if info, err := os.Lstat(filepath.Dir(targetFile)); err != nil {
		t.Fatalf("stat copied npm plugin: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("copy-mode npm plugin mode = %v, want real directory", info.Mode())
	}
	if err := os.WriteFile(targetFile, []byte("instance-plugin"), 0o644); err != nil {
		t.Fatalf("modify copied npm plugin: %v", err)
	}
	if got, err := os.ReadFile(sourceFile); err != nil {
		t.Fatalf("read default npm plugin: %v", err)
	} else if string(got) != "image-plugin" {
		t.Fatalf("default npm plugin changed through copy-mode target: %q", got)
	}
}

func TestSeedOpenClawPluginRuntimeSupportsIsolatedNPMProjects(t *testing.T) {
	root := t.TempDir()
	defaultsRoot := filepath.Join(root, "defaults", ".openclaw")
	projectName := "openclaw-feishu-dc69f44688"
	projectRoot := filepath.Join(defaultsRoot, "npm", "projects", projectName)
	feishuSource := filepath.Join(projectRoot, "node_modules", "@openclaw", "feishu")
	if err := os.MkdirAll(feishuSource, 0o755); err != nil {
		t.Fatalf("mkdir isolated npm plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(feishuSource, "index.js"), []byte("isolated-project-plugin"), 0o644); err != nil {
		t.Fatalf("write isolated npm plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "package.json"), []byte(`{"private":true}`), 0o644); err != nil {
		t.Fatalf("write isolated npm project metadata: %v", err)
	}

	pluginsSource := filepath.Join(defaultsRoot, "plugins")
	if err := os.MkdirAll(pluginsSource, 0o755); err != nil {
		t.Fatalf("mkdir plugin registry: %v", err)
	}
	registry := map[string]any{
		"installRecords": map[string]any{
			"feishu": map[string]any{
				"installPath": filepath.ToSlash(feishuSource),
			},
		},
	}
	registryData, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal plugin registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsSource, "installs.json"), registryData, 0o644); err != nil {
		t.Fatalf("write plugin registry: %v", err)
	}

	if runtime.GOOS == "windows" {
		// Windows test runners commonly cannot create symlinks without an
		// elevated token. Copy mode still exercises the new projects layout.
		t.Setenv(openClawNPMRuntimeModeEnv, openClawNPMRuntimeModeCopy)
	}
	activeRoot := filepath.Join(root, "active", ".openclaw")
	if err := seedOpenClawPluginRuntimeFrom(defaultsRoot, CreateGatewayRequest{}, activeRoot); err != nil {
		t.Fatalf("seedOpenClawPluginRuntimeFrom() error = %v", err)
	}

	feishuTarget := filepath.Join(activeRoot, "npm", "projects", projectName, "node_modules", "@openclaw", "feishu")
	if got, err := os.ReadFile(filepath.Join(feishuTarget, "index.js")); err != nil {
		t.Fatalf("read isolated npm plugin: %v", err)
	} else if string(got) != "isolated-project-plugin" {
		t.Fatalf("isolated npm plugin = %q, want image content", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(feishuTarget)
		if err != nil {
			t.Fatalf("stat shared isolated npm plugin: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("shared isolated npm plugin mode = %v, want symlink", info.Mode())
		}
	}
	projectMetadata := filepath.Join(activeRoot, "npm", "projects", projectName, "package.json")
	if info, err := os.Lstat(projectMetadata); err != nil {
		t.Fatalf("stat isolated npm project metadata: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("isolated npm project metadata mode = %v, want instance-owned file", info.Mode())
	}

	copiedRegistry, err := os.ReadFile(filepath.Join(activeRoot, "plugins", "installs.json"))
	if err != nil {
		t.Fatalf("read copied plugin registry: %v", err)
	}
	if strings.Contains(string(copiedRegistry), filepath.ToSlash(defaultsRoot)+"/") {
		t.Fatal("copied isolated-project registry still references defaults root")
	}
	if !strings.Contains(string(copiedRegistry), filepath.ToSlash(feishuTarget)) {
		t.Fatal("copied isolated-project registry does not reference instance plugin path")
	}
}

func TestSeedOpenClawPluginRuntimeRejectsUnknownNPMMode(t *testing.T) {
	t.Setenv(openClawNPMRuntimeModeEnv, "unexpected")
	defaultsRoot := filepath.Join(t.TempDir(), "defaults", ".openclaw")
	if err := os.MkdirAll(defaultsRoot, 0o755); err != nil {
		t.Fatalf("mkdir defaults root: %v", err)
	}

	err := seedOpenClawPluginRuntimeFrom(defaultsRoot, CreateGatewayRequest{}, filepath.Join(t.TempDir(), ".openclaw"))
	if err == nil || !strings.Contains(err.Error(), openClawNPMRuntimeModeEnv) {
		t.Fatalf("seedOpenClawPluginRuntimeFrom() error = %v, want invalid mode diagnostic", err)
	}
}

func TestSeedOpenClawPluginRuntimeRepairsDefaultsTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix directory traversal mode bits")
	}

	root := t.TempDir()
	defaultsParent := filepath.Join(root, "defaults")
	defaultsRoot := filepath.Join(defaultsParent, ".openclaw")
	npmRoot := filepath.Join(defaultsRoot, "npm")
	if err := os.MkdirAll(npmRoot, 0o700); err != nil {
		t.Fatalf("mkdir defaults npm root: %v", err)
	}
	for _, dir := range []string{defaultsParent, defaultsRoot, npmRoot} {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}

	t.Setenv(openClawNPMRuntimeModeEnv, openClawNPMRuntimeModeCopy)
	if err := seedOpenClawPluginRuntimeFrom(defaultsRoot, CreateGatewayRequest{}, filepath.Join(root, "active")); err != nil {
		t.Fatalf("seedOpenClawPluginRuntimeFrom() error = %v", err)
	}

	for _, dir := range []string{defaultsParent, defaultsRoot, npmRoot} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := info.Mode().Perm(); got&0o055 != 0o055 {
			t.Errorf("mode %s = %o, want group/other read and traversal bits", dir, got)
		}
	}
}

func TestWriteOpenClawGatewayConfigRejectsMismatchedInjectedPersistentDir(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspaces", "openclaw", "user-1", "instance-256")
	req := CreateGatewayRequest{
		AgentType: "openclaw", UserID: 1, InstanceID: 256,
		Environment: map[string]string{
			"CLAWMANAGER_WORKSPACE_PATH":       workspace,
			"HOME":                             filepath.Join(workspace, "home"),
			"CLAWMANAGER_AGENT_PERSISTENT_DIR": filepath.Join(root, "other-instance", ".openclaw"),
		},
	}
	err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20056)
	if err == nil || !strings.Contains(err.Error(), "CLAWMANAGER_AGENT_PERSISTENT_DIR") {
		t.Fatalf("WriteGatewayConfig() error = %v, want mismatched persistent dir error", err)
	}
}

func TestWriteOpenClawGatewayConfigRepairsMissingDingTalkOpenClawPeer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated Windows privileges")
	}

	root := t.TempDir()
	globalPackage := filepath.Join(root, "global", "openclaw")
	if err := os.MkdirAll(globalPackage, 0o755); err != nil {
		t.Fatalf("mkdir global OpenClaw package: %v", err)
	}
	previousGlobalPackage := openClawGlobalPackageDir
	openClawGlobalPackageDir = globalPackage
	t.Cleanup(func() { openClawGlobalPackageDir = previousGlobalPackage })

	defaultsRoot := filepath.Join(root, "defaults", ".openclaw")
	connectorSource := filepath.Join(defaultsRoot, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	if err := os.MkdirAll(connectorSource, 0o755); err != nil {
		t.Fatalf("mkdir default DingTalk connector: %v", err)
	}
	t.Setenv(openClawDefaultsDirEnv, defaultsRoot)

	workspace := filepath.Join(root, "workspaces", "openclaw", "user-1", "instance-350")
	activeRoot := filepath.Join(workspace, "home", ".openclaw")
	// Simulate a previously seeded instance whose npm directory exists but
	// whose OpenClaw peer link was omitted.
	connectorTarget := filepath.Join(activeRoot, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	if err := os.MkdirAll(connectorTarget, 0o755); err != nil {
		t.Fatalf("mkdir instance DingTalk connector: %v", err)
	}

	req := CreateGatewayRequest{AgentType: "openclaw", UserID: 1, InstanceID: 350}
	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20009); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	linkPath := filepath.Join(activeRoot, "npm", "node_modules", "openclaw")
	if got, err := os.Readlink(linkPath); err != nil {
		t.Fatalf("read repaired OpenClaw peer symlink: %v", err)
	} else if got != globalPackage {
		t.Fatalf("OpenClaw peer symlink = %q, want %q", got, globalPackage)
	}
	sharedLinkPath := filepath.Join(defaultsRoot, "npm", "node_modules", "openclaw")
	if got, err := os.Readlink(sharedLinkPath); err != nil {
		t.Fatalf("read shared OpenClaw peer symlink: %v", err)
	} else if got != globalPackage {
		t.Fatalf("shared OpenClaw peer symlink = %q, want %q", got, globalPackage)
	}

	if err := WriteGatewayConfig(Config{GatewayAuthMode: "trusted-proxy"}, req, workspace, 20009); err != nil {
		t.Fatalf("second WriteGatewayConfig() error = %v", err)
	}
	if got, err := os.Readlink(linkPath); err != nil {
		t.Fatalf("read idempotent OpenClaw peer symlink: %v", err)
	} else if got != globalPackage {
		t.Fatalf("idempotent OpenClaw peer symlink = %q, want %q", got, globalPackage)
	}
}

func TestEnsureDingTalkOpenClawPeerConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated Windows privileges")
	}

	root := t.TempDir()
	globalPackage := filepath.Join(root, "global", "openclaw")
	if err := os.MkdirAll(globalPackage, 0o755); err != nil {
		t.Fatalf("mkdir global OpenClaw package: %v", err)
	}
	previousGlobalPackage := openClawGlobalPackageDir
	openClawGlobalPackageDir = globalPackage
	defer func() { openClawGlobalPackageDir = previousGlobalPackage }()

	activeRoot := filepath.Join(root, ".openclaw")
	connectorDir := filepath.Join(activeRoot, "npm", "node_modules", "@dingtalk-real-ai", "dingtalk-connector")
	if err := os.MkdirAll(connectorDir, 0o755); err != nil {
		t.Fatalf("mkdir DingTalk connector: %v", err)
	}

	const workers = 32
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureDingTalkOpenClawPeer(activeRoot)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ensureDingTalkOpenClawPeer() error = %v", err)
		}
	}

	linkPath := filepath.Join(activeRoot, "npm", "node_modules", "openclaw")
	if got, err := os.Readlink(linkPath); err != nil {
		t.Fatalf("read concurrent OpenClaw peer symlink: %v", err)
	} else if got != globalPackage {
		t.Fatalf("concurrent OpenClaw peer symlink = %q, want %q", got, globalPackage)
	}
}
