package supervisor

import (
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"testing"
)

func TestOpenClawCapabilityHealthReporting(t *testing.T) {
	ready := false
	if runtimeSnapshotHealth(process.Snapshot{Status: process.StatusConfiguring, CapabilityReportReady: &ready}) != "degraded" {
		t.Fatal("capability failure reported healthy")
	}
	ready = true
	if runtimeSnapshotHealth(process.Snapshot{Status: process.StatusRunning, CapabilityReportReady: &ready}) != "ok" {
		t.Fatal("healthy capability degraded")
	}
	if runtimeSnapshotHealth(process.Snapshot{Status: process.StatusConfiguring}) != "ok" {
		t.Fatal("legacy changed")
	}
}
