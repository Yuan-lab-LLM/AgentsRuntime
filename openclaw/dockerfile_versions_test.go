package openclaw_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const targetOpenClawVersion = "2026.7.1-2"

func TestImagePinsOpenClawCompatibleRuntimeAndChannelPlugins(t *testing.T) {
	content, err := os.ReadFile("Dockerfile.openclaw")
	if err != nil {
		t.Fatalf("read Dockerfile.openclaw: %v", err)
	}
	dockerfile := string(content)

	required := []string{
		"FROM node:22-bookworm-slim@sha256:53ada149d435c38b14476cb57e4a7da73c15595aba79bd6971b547ceb6d018bf AS node-runtime",
		"RUN test \"$(node --version)\" = \"v22.23.1\"",
		"npm install -g openclaw@" + targetOpenClawVersion,
		"patch_memory_core_startup_migration.mjs --patch",
		"patch_memory_core_startup_migration.mjs --verify",
		"test_memory_core_startup_migration.mjs",
		"patch_legacy_task_sidecar_startup_migration.mjs --patch",
		"patch_legacy_task_sidecar_startup_migration.mjs --verify",
		"test_legacy_task_sidecar_startup_migration.mjs",
		"openclaw plugins install @dingtalk-real-ai/dingtalk-connector@0.8.24",
		"openclaw plugins install @wecom/wecom-openclaw-plugin@2026.7.2-beta.1",
		"openclaw plugins install @openclaw/feishu@2026.7.1",
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

	if pkg.Version != "0.2.2" {
		t.Errorf("Redis Team package version = %q, want %q", pkg.Version, "0.2.2")
	}
	if pkg.OpenClaw.Compat.PluginAPI != ">=2026.5.4" {
		t.Errorf("Redis Team plugin API range = %q, want old/new Runtime compatible lower bound", pkg.OpenClaw.Compat.PluginAPI)
	}
	if pkg.OpenClaw.Build.OpenClawVersion != targetOpenClawVersion {
		t.Errorf("Redis Team OpenClaw build baseline = %q, want %q", pkg.OpenClaw.Build.OpenClawVersion, targetOpenClawVersion)
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
		t.Fatal("OpenClaw 2026.7.1 browser plugin must be enabled with the browser runtime")
	}
}
