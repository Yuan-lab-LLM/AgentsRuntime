package process

import (
	"context"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"os/exec"
	"testing"
	"time"
)

func awaitCapabilityTest(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not settle")
	}
}
func TestOpenClawCapabilityMonitorSkipsClosedProcess(t *testing.T) {
	m := New(appconfig.Config{})
	done := make(chan struct{})
	close(done)
	m.monitorCapabilitiesWithProbe(&exec.Cmd{}, done, func(context.Context, []string) error { t.Fatal("closed process sent request"); return nil }, time.Millisecond)
}
func TestOpenClawCapabilityMonitorStopCancelsInflightRequest(t *testing.T) {
	cmd := &exec.Cmd{}
	m := New(appconfig.Config{})
	m.cmd = cmd
	m.status = StatusRunning
	entered, finished, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}()
	go func() {
		defer close(finished)
		m.monitorCapabilitiesWithProbe(cmd, done, func(ctx context.Context, _ []string) error { close(entered); <-ctx.Done(); return ctx.Err() }, time.Millisecond)
	}()
	awaitCapabilityTest(t, entered)
	close(done)
	awaitCapabilityTest(t, finished)
	if !m.capabilityCheckedAt.IsZero() {
		t.Fatal("cancelled response published an observation")
	}
}
func TestOpenClawCapabilityMonitorRestartRejectsLateSuccess(t *testing.T) {
	cmd, next := &exec.Cmd{}, &exec.Cmd{}
	m := New(appconfig.Config{})
	m.cmd = cmd
	m.status = StatusRunning
	entered, release, finished, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(done)
	go func() {
		defer close(finished)
		m.monitorCapabilitiesWithProbe(cmd, done, func(context.Context, []string) error { close(entered); <-release; return nil }, time.Millisecond)
	}()
	awaitCapabilityTest(t, entered)
	m.mu.Lock()
	m.cmd = next
	m.mu.Unlock()
	close(release)
	awaitCapabilityTest(t, finished)
	if m.capabilityCommand != nil || !m.capabilityCheckedAt.IsZero() {
		t.Fatal("old response changed new process readiness")
	}
}
func TestOpenClawCapabilityMonitorFutureObservationUnavailable(t *testing.T) {
	cmd := &exec.Cmd{Env: []string{renewalFlag + "=1"}}
	m := New(appconfig.Config{})
	m.cmd = cmd
	m.status = StatusRunning
	m.recordCapabilityObservation(cmd, true, time.Now().Add(time.Minute))
	s := m.Snapshot()
	if s.Status != StatusConfiguring || s.CapabilityReportReady == nil || *s.CapabilityReportReady {
		t.Fatal("future observation was accepted")
	}
}
