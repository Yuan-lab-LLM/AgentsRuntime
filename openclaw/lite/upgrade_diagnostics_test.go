package openclaw

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRemapsPluginPathsButPreservesImageOwnedBindings(t *testing.T) {
	source := filepath.Join(t.TempDir(), "live")
	probe := filepath.Join(t.TempDir(), "probe")
	paths := []any{filepath.Join(source, "extensions", "custom"), "/usr/local/lib/node_modules/openclaw/dist/extensions/redis-team"}
	record := map[string]any{"installPath": filepath.Join(source, "npm", "package")}
	config := map[string]any{"plugins": map[string]any{"load": map[string]any{"paths": paths}, "installs": map[string]any{"custom": record}}}
	if err := remapProbeControlPaths(config, source, probe); err != nil {
		t.Fatal(err)
	}
	if paths[0] != filepath.Join(probe, "extensions", "custom") || record["installPath"] != filepath.Join(probe, "npm", "package") {
		t.Fatal("probe still refers to live plugin paths")
	}
	if paths[1] != "/usr/local/lib/node_modules/openclaw/dist/extensions/redis-team" {
		t.Fatal("image-owned Team plugin binding changed")
	}
}

func TestDoctorSummaryPreservesWrappedPluginFailure(t *testing.T) {
	output := `│ - Plugin "feishu" failed post-core payload smoke check             │
│   (peer-mismatch): installed peer resolves outside the host root. │
│   /private/home/.openclaw/npm/project token=do-not-disclose        │
│ - Plugin "wecom-openclaw-plugin" failed post-core payload smoke check │
│   (entry-missing): compiled runtime entry does not exist          │
└──────────────────────────────────────────────────────────────────┘
unrelated user conversation must not enter diagnostics`
	got := safeOpenClawFailureSummary([]byte(output))
	for _, want := range []string{"peer-mismatch", "entry-missing", "feishu", "wecom-openclaw-plugin"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, secret := range []string{"/private/home", "do-not-disclose", "unrelated user conversation"} {
		if strings.Contains(got, secret) {
			t.Errorf("diagnostic leaked %q", secret)
		}
	}
}

func TestUpgradeReceiptsFenceGenerationAndRetainLegacyCompatibility(t *testing.T) {
	req := WorkspaceUpgradeRequest{InstanceID: 1, RolloutID: "current", Generation: 3}
	good := SessionSQLiteMigrationResult{InstanceID: 1, RolloutID: "current", Generation: 3, Status: "validated"}
	if !sessionMigrationReceiptMatches(good, req) {
		t.Fatal("current receipt rejected")
	}
	for _, stale := range []SessionSQLiteMigrationResult{{InstanceID: 1, RolloutID: "other", Generation: 3}, {InstanceID: 1, RolloutID: "current", Generation: 2}, {InstanceID: 2}} {
		if sessionMigrationReceiptMatches(stale, req) {
			t.Fatal("foreign execution receipt accepted")
		}
	}
	if !sessionMigrationReceiptMatches(SessionSQLiteMigrationResult{InstanceID: 1}, req) {
		t.Fatal("legacy receipt rejected")
	}
	if sessionRestoreReceiptMatches(SessionSQLiteRestoreResult{InstanceID: 1, RolloutID: "current", Generation: 2, Status: "restored"}, req) {
		t.Fatal("stale restore receipt accepted")
	}
}
