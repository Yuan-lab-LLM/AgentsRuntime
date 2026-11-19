package process

import (
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"os/exec"
	"testing"
	"time"
)

func TestOpenClawCapabilityMonitorAvailabilityAndIdentity(t *testing.T) {
	cmd := &exec.Cmd{Env: []string{renewalFlag + "=1"}}
	m := New(appconfig.Config{})
	m.cmd = cmd
	m.status = StatusRunning
	if m.Snapshot().Status != StatusConfiguring {
		t.Fatal("unchecked report marked available")
	}
	if !m.recordCapabilityObservation(cmd, true, time.Now()) || m.Snapshot().Status != StatusRunning {
		t.Fatal("fresh report not restored")
	}
	m.recordCapabilityObservation(cmd, false, time.Now())
	if m.Snapshot().Status != StatusConfiguring || m.status != StatusRunning {
		t.Fatal("failure did not degrade or mutated lifecycle")
	}
	m.recordCapabilityObservation(cmd, true, time.Now().Add(-16*time.Second))
	if m.Snapshot().Status != StatusConfiguring {
		t.Fatal("stale observation available")
	}
	next := &exec.Cmd{Env: cmd.Env}
	m.cmd = next
	if m.recordCapabilityObservation(cmd, true, time.Now()) {
		t.Fatal("old process modified new identity")
	}
	if m.Snapshot().Status != StatusConfiguring {
		t.Fatal("old readiness reused")
	}
	m.recordCapabilityObservation(next, true, time.Now())
	if m.Snapshot().Status != StatusRunning {
		t.Fatal("new report not restored")
	}
	for _, state := range []Status{StatusStopping, StatusStopped, StatusCrashed} {
		m.status = state
		if m.recordCapabilityObservation(next, true, time.Now()) || m.Snapshot().Status != state {
			t.Fatal("monitor overwrote stopped state")
		}
	}
	m.cmd = &exec.Cmd{}
	m.status = StatusRunning
	if m.Snapshot().Status != StatusRunning {
		t.Fatal("legacy changed")
	}
}
