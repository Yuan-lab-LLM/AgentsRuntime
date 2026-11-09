package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"runtime"
	"strings"
)

var openClaw81Capabilities = []string{
	"openclaw.state.sqlite",
	"openclaw.automation.rpc",
	"openclaw.backup.sqlite",
	"openclaw.database.preflight",
	"openclaw.plugin.capability-consent",
	"openclaw.gateway.stop-confirm",
	"openclaw.workspace.writer-lease",
	"openclaw.session-sqlite-migrate-v1",
	"openclaw.session-sqlite-restore-v1",
	"openclaw.session-continuity-v1",
	"openclaw.runtime-standby-v1",
	"openclaw.upgrade-capsule-v2",
	"openclaw.upgrade-capsule-v3",
	"openclaw.session-sqlite-preserve-v1",
	"openclaw.upgrade-preflight-v3",
	"openclaw.upgrade-preflight-v4",
	"redis-team.group-hooks-v1",
}

func OpenClawCapabilities(version string) []string {
	if !IsOpenClawAtLeast(version, OpenClaw81Version) {
		return nil
	}
	capabilities := append([]string(nil), openClaw81Capabilities...)
	if runtime.GOOS == "linux" {
		capabilities = append(capabilities, "openclaw.upgrade-operation-fence-v1", "openclaw.rebuild-preserve-v1", "openclaw.rebuild-preserve-v2")
	}
	if runtime.GOOS == "linux" && version == "2026.10.2" {
		capabilities = append(capabilities, "openclaw.managed-plugin-convergence-v1")
	}
	return capabilities
}

func (p Profile) RegistrationMetadata(cfg gateway.Config) gateway.RuntimeRegistrationMetadata {
	store := "jsonl"
	if IsOpenClawAtLeast(cfg.OpenClawVersion, OpenClaw81Version) {
		store = "sqlite"
	}
	return gateway.RuntimeRegistrationMetadata{OpenClawVersion: cfg.OpenClawVersion, Capabilities: OpenClawCapabilities(cfg.OpenClawVersion), ProtocolVersion: "openclaw-upgrade-v3", TeamPluginVersion: strings.TrimSpace(os.Getenv("CLAWMANAGER_REDIS_TEAM_PLUGIN_VERSION")), SessionStore: store}
}
