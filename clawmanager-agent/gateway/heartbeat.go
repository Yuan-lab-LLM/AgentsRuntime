package gateway

import (
	"context"
	"log"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/heartbeat"
)

func (m *GatewayManager) superviseGateway(ctx context.Context, gatewayID string, spec GatewayStartSpec, pid int) {
	if _, noop := m.health.(noopGatewayHealthChecker); noop {
		return
	}
	failures := heartbeat.NewFailureTracker(heartbeat.DefaultFailureThreshold)
	timer := time.NewTimer(m.nextGatewayHealthDelay())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		m.mu.RLock()
		record := m.gateways[gatewayID]
		active := record != nil && record.process.PID == pid && (record.state.State == "running" || record.state.State == "unhealthy")
		m.mu.RUnlock()
		if !active {
			return
		}
		probeErr := m.probeGatewayHealth(ctx, spec, pid)
		if ctx.Err() != nil {
			return
		}
		if !m.observeGatewayHealth(gatewayID, spec.InstanceID, spec.Generation, pid, &failures, probeErr) {
			return
		}
		timer.Reset(m.nextGatewayHealthDelay())
	}
}

func (m *GatewayManager) nextGatewayHealthDelay() time.Duration {
	if m.nextGatewayHealthCheckDelay != nil {
		if delay := m.nextGatewayHealthCheckDelay(); delay > 0 {
			return delay
		}
	}
	interval := m.cfg.GatewayReportInterval
	if interval <= 0 {
		interval = heartbeat.DefaultInterval
	}
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	return heartbeat.NextDelay(interval)
}

func (m *GatewayManager) probeGatewayHealth(ctx context.Context, spec GatewayStartSpec, pid int) error {
	if err := m.Health(); err != nil && safeGatewayError(err) != "clock_reference_unavailable" {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m.mu.RLock()
	record := m.gateways[spec.GatewayID]
	var verify func(context.Context) error
	if record != nil && record.process.PID == pid {
		verify = record.process.VerifyListeners
	}
	m.mu.RUnlock()
	if verify != nil {
		if err := verify(probeCtx); err != nil {
			return err
		}
	}
	if verifier, ok := m.starter.(interface {
		VerifyListener(GatewayStartSpec, int) error
	}); ok {
		if err := verifier.VerifyListener(spec, pid); err != nil {
			return err
		}
	}
	if err := m.health.CheckHealth(probeCtx, spec); err != nil {
		return err
	}
	return nil
}

func (m *GatewayManager) observeGatewayHealth(gatewayID string, instanceID, generation, pid int, failures *heartbeat.FailureTracker, probeErr error) bool {
	observation := failures.Observe(probeErr)
	if probeErr != nil && !observation.Unavailable {
		log.Printf("runtime-agent gateway health probe deferred: instance_id=%d generation=%d consecutive_failures=%d error=%s", instanceID, generation, observation.ConsecutiveFailures, safeGatewayHealthError(probeErr))
		return true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.gateways[gatewayID]
	if record == nil {
		return false
	}
	if record.process.PID != pid || record.state.InstanceID != instanceID || record.state.Generation != generation || (record.state.State != "running" && record.state.State != "unhealthy") {
		return false
	}

	now := time.Now().UTC()
	changed := false
	if probeErr != nil {
		errorCode := safeGatewayHealthError(probeErr)
		changed = record.state.State != "unhealthy" || record.state.ErrorMessage != errorCode
		record.state.State = "unhealthy"
		record.state.ErrorMessage = errorCode
	} else {
		changed = record.state.State == "unhealthy"
		record.state.State = "running"
		if changed {
			record.state.ErrorMessage = ""
		}
		record.state.HealthAt = now
	}
	record.state.UpdatedAt = now
	if changed {
		m.notifyGatewayStateChangedLocked()
	}
	return true
}

func safeGatewayHealthError(err error) string {
	code := safeGatewayError(err)
	if code == "gateway_start_failed" {
		return "gateway_health_unavailable"
	}
	return code
}
