package process

import (
	"context"
	"os/exec"
	"time"
)

// A liveness observation is neither a cancellation receipt nor authority to
// kill/restart a running task. Only the exported availability is degraded.
func (m *Manager) recordCapabilityObservation(cmd *exec.Cmd, ready bool, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != cmd || cmd == nil || m.status == StatusStopped || m.status == StatusStopping || m.status == StatusCrashed {
		return false
	}
	m.capabilityCommand = cmd
	m.capabilityReady = ready
	m.capabilityCheckedAt = now
	return true
}
func (m *Manager) monitorCapabilities(cmd *exec.Cmd, done <-chan struct{}) {
	m.monitorCapabilitiesWithProbe(cmd, done, checkCapabilityHealth, 5*time.Second)
}
func (m *Manager) monitorCapabilitiesWithProbe(cmd *exec.Cmd, done <-chan struct{}, probe func(context.Context, []string) error, interval time.Duration) {
	if done == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	if cmd == nil || probe == nil || interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	env := append([]string(nil), cmd.Env...)
	for {
		if ctx.Err() != nil {
			return
		}
		ready := probe(ctx, env) == nil
		if ctx.Err() != nil || !m.recordCapabilityObservation(cmd, ready, time.Now()) {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
