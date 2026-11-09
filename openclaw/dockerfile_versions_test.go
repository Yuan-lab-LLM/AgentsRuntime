package openclaw_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const targetOpenClawVersion = "2026.10.2"

func TestImagePinsOpenClawCompatibleRuntimeAndChannelPlugins(t *testing.T) {
	content, err := os.ReadFile("Dockerfile.openclaw")
	if err != nil {
		t.Fatalf("read Dockerfile.openclaw: %v", err)
	}
	dockerfile := string(content)

	required := []string{
		"FROM lscr.io/linuxserver/webtop:ubuntu-kde-version-cc2e18a4@sha256:5005ffafe50117dfd8b28e3c37efd2cf603e37bc3741d472b1540739492d3ee0",
		"FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS node-runtime",
		"RUN test \"$(node --version)\" = \"v24.21.0\"",
		"node /pinned/scripts/install-pinned-openclaw.mjs",
		"patch_memory_core_startup_migration.mjs --patch",
		"patch_memory_core_startup_migration.mjs --verify",
		"test_memory_core_startup_migration.mjs",
		"test_exec_approvals_doctor_migration.mjs",
		"patch_workspace_publication_102.mjs --patch",
		"patch_workspace_publication_102.mjs --verify",
		"test_workspace_publication_102.mjs",
		"install-openclaw-plugin @dingtalk-real-ai/dingtalk-connector@0.8.26 --force --accept-capabilities",
		"install-openclaw-plugin @wecom/wecom-openclaw-plugin@2026.9.15 --force --accept-capabilities",
		"install-openclaw-plugin @openclaw/feishu@2026.10.2 --force --accept-capabilities",
		"install-openclaw-plugin \"${redis_team_tgz}\" --force --accept-capabilities",
		"if HOME=/defaults install-openclaw-plugin \"${redis_team_tgz}\" --force >/tmp/plugin-consent-negative.log 2>&1; then exit 1; fi",
		"ENV CLAWMANAGER_OPENCLAW_VERSION=2026.10.2",
		"io.clawmanager.runtime.type=\"openclaw\"",
		"io.clawmanager.openclaw.version=\"2026.10.2\"",
		"io.clawmanager.upgrade.strategy=\"openclaw-sqlite-v1\"",
		"io.clawmanager.upgrade.protocol=\"openclaw-upgrade-v3\"",
		"ENV CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION=0.3.1",
		"HOME=/defaults openclaw config validate",
	}
	for _, fragment := range required {
		if !strings.Contains(dockerfile, fragment) {
			t.Errorf("Dockerfile.openclaw is missing pinned dependency %q", fragment)
		}
	}

	forbidden := []string{
		"FROM node:22-bookworm-slim AS node-runtime",
		"openclaw@v2026.5.4",
		"openclaw plugins install @dingtalk-real-ai/dingtalk-connector ",
		"openclaw plugins install @wecom/wecom-openclaw-plugin@beta",
		"openclaw plugins install @openclaw/feishu@2026.5.4",
		"patch_legacy_task_sidecar_startup_migration.mjs",
		"test_legacy_task_sidecar_startup_migration.mjs",
		"__clawmanagerBrowserProxyMode",
	}
	for _, fragment := range forbidden {
		if strings.Contains(dockerfile, fragment) {
			t.Errorf("Dockerfile.openclaw must not retain floating or obsolete dependency %q", fragment)
		}
	}
}

func TestRedisTeamPluginDeclaresTargetOpenClawBuildBaseline(t *testing.T) {
	content, err := os.ReadFile("../plugins/openclaw-redis-team/package.json")
	if err != nil {
		t.Fatalf("read Redis Team package.json: %v", err)
	}

	var pkg struct {
		Version  string `json:"version"`
		OpenClaw struct {
			Compat struct {
				PluginAPI string `json:"pluginApi"`
			} `json:"compat"`
			Build struct {
				OpenClawVersion string `json:"openclawVersion"`
			} `json:"build"`
		} `json:"openclaw"`
	}
	if err := json.Unmarshal(content, &pkg); err != nil {
		t.Fatalf("parse Redis Team package.json: %v", err)
	}

	if pkg.Version != "0.3.1" {
		t.Errorf("Redis Team package version = %q, want %q", pkg.Version, "0.3.1")
	}
	if pkg.OpenClaw.Compat.PluginAPI != ">=2026.8.1" {
		t.Errorf("Redis Team plugin API range = %q, want 8.1 Runtime lower bound", pkg.OpenClaw.Compat.PluginAPI)
	}
	if pkg.OpenClaw.Build.OpenClawVersion != targetOpenClawVersion {
		t.Errorf("Redis Team OpenClaw build baseline = %q, want %q", pkg.OpenClaw.Build.OpenClawVersion, targetOpenClawVersion)
	}
}

func TestOpenClawServicePreparesWritableSQLiteFallbackCache(t *testing.T) {
	content, err := os.ReadFile("scripts/openclaw-agent-run")
	if err != nil {
		t.Fatalf("read openclaw-agent service: %v", err)
	}
	script := string(content)
	for _, fragment := range []string{
		"mkdir -p \"$AGENT_DATA_DIR\" \"$AGENT_LOG_DIR\" \"$AGENT_CONFIG_DIR\" /config/.cache",
		"chown \"$AGENT_USER:$AGENT_USER\" /config/.cache",
		"chmod 0700 /config/.cache",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("openclaw-agent service missing SQLite fallback cache preparation %q", fragment)
		}
	}
}

func TestEveryExternalPluginInstallAcceptsCapabilities(t *testing.T) {
	content, err := os.ReadFile("Dockerfile.openclaw")
	if err != nil {
		t.Fatal(err)
	}
	installCount := 0
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.Contains(line, "HOME=/defaults install-openclaw-plugin") || strings.Contains(line, "plugin-consent-negative.log") {
			continue
		}
		installCount++
		if !strings.Contains(line, "--accept-capabilities") {
			t.Fatalf("plugin install is not fail-closed on capability consent: %s", strings.TrimSpace(line))
		}
	}
	if installCount != 4 {
		t.Fatalf("plugin install count = %d, want 4 pinned installs", installCount)
	}
}

func TestPluginInstallGateRejectsMissingCapabilityConsent(t *testing.T) {
	content, err := os.ReadFile("scripts/install-openclaw-plugin")
	if err != nil {
		t.Fatalf("read plugin install gate: %v", err)
	}
	script := string(content)
	for _, fragment := range []string{
		`if [ "$argument" = "--accept-capabilities" ]`,
		`if [ "$consent" != "true" ]`,
		"exit 64",
		`exec openclaw plugins install "$@"`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("plugin install gate missing fail-closed contract %q", fragment)
		}
	}
}

func TestDefaultConfigEnablesBrowserPluginWithBrowserRuntime(t *testing.T) {
	content, err := os.ReadFile("defaults-template/.openclaw/openclaw.json")
	if err != nil {
		t.Fatalf("read default OpenClaw config: %v", err)
	}
	var config struct {
		Browser struct {
			Enabled bool `json:"enabled"`
		} `json:"browser"`
		Plugins struct {
			Entries map[string]struct {
				Enabled bool `json:"enabled"`
			} `json:"entries"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("parse default OpenClaw config: %v", err)
	}
	if !config.Browser.Enabled {
		t.Fatal("default browser runtime must remain enabled")
	}
	if !config.Plugins.Entries["browser"].Enabled {
		t.Fatal("OpenClaw 2026.8.1 browser plugin must be enabled with the browser runtime")
	}
}
