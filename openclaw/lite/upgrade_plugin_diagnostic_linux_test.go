//go:build linux

package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/openclawcompat"
)

// Opt-in disposable fixture only. Preserve the probe after a failure so the
// local test can explain a diagnostic omitted from the production-safe summary.
func TestUpgradePluginDisposableDoctorDiagnostic(t *testing.T) {
	if os.Getenv("UPGRADE_PLUGIN_DISPOSABLE_DIAGNOSTIC") != "1" {
		t.Skip("disposable fixture only")
	}
	root := "/workspaces"
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: "2026.10.2", WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "diagnostic-probe", InstanceID: 911, UserID: 1, Generation: 1, UID: 200911, GID: 200911}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "seed-2026.8.1.json")); err != nil {
		t.Fatal("not a seeded disposable fixture")
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	openclawcompat.Normalize81(config)
	probe, home, _, err := mgr.createUpgradeCompatibilityProbe(workspace, config, req)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cfg := filepath.Join(home, ".openclaw", "openclaw.json")
	err = runOpenClawCommandInHome(ctx, probe, home, cfg, req, "probe-plugin-metadata", []string{filepath.Join(workspace, "home", ".openclaw"), filepath.Join(home, ".openclaw")}, &output, 2*time.Minute)
	if err == nil {
		err = runOpenClawDoctorRepair(ctx, probe, home, cfg, req, &output)
	}
	if err != nil {
		t.Logf("retained local probe %s\n%s", probe, output.String())
		t.Fatal(err)
	}
	mgr.removeUpgradeProbe(probe)
}
